package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/state"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// DiscordAttachmentLimit is the Discord free-tier per-file upload
// cap. Discord lowered this from 25 MB to 10 MB in 2024-09 (see
// https://hardware.slashdot.org/story/24/09/05/0149255 and the
// Discord support article). M7 enforces this server-side per
// attachment: any single file larger than this returns
// `ATTACHMENT_TOO_LARGE`. Boosted guilds (level 2 / level 3) and
// Nitro callers may actually upload more; the agentchat-side
// guard stays at the floor so the failure mode is a deterministic
// 413 from us rather than a noisy 503 from Discord. Operators
// running on a boosted guild can patch this constant locally.
const DiscordAttachmentLimit = 10 * 1024 * 1024

// SendMessage handles POST /v1/rooms/{id}/messages.
//
// Sequence:
//  1. Validate request shape (priority known; content or attachment).
//  2. Read tx: resolve room (must not be archived); read actor's role;
//     RoleUser must be a member of the room (PERM_DENIED otherwise),
//     RoleAdmin bypasses the membership gate per requirements §5.1
//     "发：admin 不受限" (M4-P3-004). Resolve reply parent if any.
//  3. Validate attachment paths and per-file sizes (after authz).
//  4. Acquire actor's Provider and call Provider.SendMessage (slow,
//     outside tx).
//  5. Write tx:
//     - INSERT-or-IGNORE the message row by discord_msg_id (M4 dedupe
//     against the matching gateway echo that arrives via the
//     ingester). Either path produces the same persisted row.
//     - Fan out one message_states row per **member** of the room
//     (subscribed AND unsubscribed; requirements §4 / §5.1) with
//     read_at=now only for the author (M4-P3-005).
//     - Upsert the author's own message_state with read_at=now
//     unconditionally, so admin overrides who are NOT members still
//     have a state row (M4-P3-005 / M4-P3-007).
//     - Write the message.send audit row using the persisted id
//     regardless of which path won the insert race (M4-P3-007). The
//     payload carries race_with_ingest so audit forensics can tell.
func SendMessage(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		var req SendMessageRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		// M7: an attachment-only message is allowed (Discord renders
		// the file as the visible body). Content + zero attachments
		// is the only invalid combination.
		if req.Content == "" && len(req.Attachments) == 0 {
			WriteError(w, errcode.New(errcode.InvalidArgument,
				"content is empty and no attachments provided"))
			return
		}
		priority := store.PriorityNormal
		if req.Priority != "" {
			priority = store.MessagePriority(req.Priority)
			if !priority.Valid() {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"invalid priority %q", req.Priority))
				return
			}
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		// M7-P3-001 fix: attachment path / size validation must come
		// AFTER room authorization, otherwise any authenticated token
		// can probe daemon-local filesystem paths and file sizes via
		// the response code (400 vs 413 vs ...). The cheap-rejection
		// argument in the original M7 phase1 §3.3 doesn't outweigh
		// the information leak. Order is now:
		//   1) request-shape validation (already above)
		//   2) WithTx: room exists + not archived + caller is member
		//      (or admin) + reply target resolution
		//   3) attachment stat + per-file size guard (NEW position)
		//   4) acquire Provider
		//   5) Provider.SendMessage
		// Reserve `uploads` / `uploadSizes` here so the assemble step
		// below can populate them after authz succeeds.
		var (
			uploads     []bot.UploadFile
			uploadSizes []int64
		)

		// Resolve room + actor role + membership + reply target inside
		// a quick read-only WithTx so we don't accidentally see a torn
		// view.
		//
		// Membership gate (M4-P3-004 fix): per requirements §5.1
		// "发：admin 不受限；user 只能在自己所属的群里发". Admins are
		// NOT required to be a member of the room. Real Discord
		// permission failures still surface from Provider.SendMessage.
		var (
			room         *store.Room
			replyDiscord string
			replyParent  *store.Message
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			rr, err := b.Rooms.Get(r.Context(), roomID)
			if err != nil {
				return err
			}
			if rr.Archived {
				return errcode.New(errcode.Conflict,
					"room %s is archived", roomID)
			}
			room = rr
			a, err := b.Accounts.Get(r.Context(), actor.ID)
			if err != nil {
				return err
			}
			if a.Role != store.RoleAdmin {
				// M8-S-P2-012: priority=system is operator/announcement
				// territory. Non-admin callers cannot impersonate
				// system traffic. Fail before the membership probe so
				// the response code reflects the actual policy
				// violation rather than masking it behind "not a
				// member" — and so the membership read is skipped on
				// disallowed inputs.
				if priority == store.PrioritySystem {
					return errcode.New(errcode.PermDenied,
						"priority=system requires admin role")
				}
				if _, err := b.Memberships.Get(r.Context(), actor.ID, roomID); err != nil {
					if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
						return errcode.New(errcode.PermDenied,
							"actor %s is not a member of room %s", actor.ID, roomID)
					}
					return err
				}
			}
			if req.ReplyToID != "" {
				parent, err := b.Messages.Get(r.Context(), req.ReplyToID)
				if err != nil {
					return err
				}
				if parent.RoomID != roomID {
					return errcode.New(errcode.InvalidArgument,
						"reply_to_id %s is in a different room", req.ReplyToID)
				}
				replyDiscord = parent.DiscordMsgID
				replyParent = parent
			}
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}

		// M7 attachment pre-flight — runs AFTER authz (room exists +
		// member-or-admin). The per-file size guard enforces the
		// `DiscordAttachmentLimit` constant per attachment, not
		// aggregate (M7-P3-002 fix). The current value tracks
		// Discord's free-tier per-file cap (10 MB as of 2024-09 —
		// see the constant doc above).
		uploads = make([]bot.UploadFile, 0, len(req.Attachments))
		uploadSizes = make([]int64, 0, len(req.Attachments))
		for _, a := range req.Attachments {
			if a.Path == "" {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"attachment.path is required"))
				return
			}
			// Lstat first (M8-S-P2-004): a symlink is a TOCTOU oracle —
			// between this stat and the bot.Provider open, an attacker
			// with write access to the parent directory can swap the
			// target for /etc/shadow / the daemon's own master.key. We
			// reject symlinks outright rather than canonicalising.
			fi, statErr := os.Lstat(a.Path)
			if statErr != nil {
				WriteError(w, errcode.Wrap(statErr, errcode.InvalidArgument,
					"stat attachment %s", a.Path))
				return
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"attachment %s is a symlink; refuse to follow", a.Path))
				return
			}
			if !fi.Mode().IsRegular() {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"attachment %s is not a regular file", a.Path))
				return
			}
			if fi.IsDir() {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"attachment %s is a directory", a.Path))
				return
			}
			sz := fi.Size()
			if sz > DiscordAttachmentLimit {
				WriteError(w, errcode.New(errcode.AttachmentTooLarge,
					"attachment %s exceeds Discord per-file limit (%d bytes > %d)",
					a.Path, sz, DiscordAttachmentLimit))
				return
			}
			fname := a.Filename
			if fname == "" {
				fname = filepath.Base(a.Path)
			}
			uploads = append(uploads, bot.UploadFile{
				Path:     a.Path,
				FileName: fname,
				MIME:     a.MIME,
			})
			uploadSizes = append(uploadSizes, sz)
		}

		p, err := providerForActor(conn, actor.ID)
		if err != nil {
			WriteError(w, err)
			return
		}

		// Slow: Discord send. Outside tx.
		sent, err := p.SendMessage(r.Context(), room.DiscordChannelID, req.Content, bot.SendOptions{
			ReplyToMessageID: replyDiscord,
			Attachments:      uploads,
		})
		if err != nil {
			WriteError(w, err)
			return
		}

		// Persist + fan-out states.
		msgID, err := uuid.NewV7()
		if err != nil {
			WriteError(w, errcode.Wrap(err, errcode.Internal, "uuidv7"))
			return
		}
		hash := sha256.Sum256([]byte(req.Content))
		now := time.Now().UTC()
		msg := &store.Message{
			ID:              msgID.String(),
			RoomID:          roomID,
			AuthorAccountID: actor.ID,
			DiscordMsgID:    sent.ID,
			Content:         req.Content,
			Priority:        priority,
			RequiresAck:     req.RequiresAck,
			CreatedAt:       sent.CreatedAt.UTC(),
			ContentHash:     hex.EncodeToString(hash[:]),
			MentionAll:      req.MentionAll,
			// M9 Phase 1: mirror --all to MentionEveryone so the new
			// state.CountMentionsForSubscribed (which reads
			// mention_everyone) sees legacy send-path @all without
			// requiring the API surface to change yet. Phase 2 drops
			// MentionAll entirely.
			MentionEveryone: req.MentionAll,
		}
		if replyParent != nil {
			msg.ReplyToMsgID = replyParent.ID
		}
		var persisted *store.Message
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			persistedID, inserted, err := b.Messages.CreateIgnoreConflict(r.Context(), msg)
			if err != nil {
				return err
			}
			// The send-path always owns the canonical message_state for
			// the author (read_at = now) and always writes the
			// message.send audit, regardless of which path won the
			// insert race against the ingester (M4-P3-007 fix).
			//
			// fan-out scope (M4-P3-005 fix): per requirements §5.1
			// "订阅与否仅影响通知，不影响是否收到". Every current
			// member of the room — subscribed or unsubscribed (旁观)
			// — gets a message_state row. M5 decides what to render in
			// the primary vs secondary state UI.
			effectiveID := persistedID
			if inserted {
				persisted = msg
			} else {
				// Ingester won the discord_msg_id race. The row it
				// created carries Discord-native fields only; the
				// send path owns agentchat-local metadata and must
				// apply it now (M4-P3-010 fix). Without this the API
				// returns 201 but the stored row silently keeps the
				// ingester defaults (requires_ack=false, priority=
				// normal, no reply, no author_account_id).
				if err := b.Messages.ApplySendMetadata(r.Context(), persistedID, store.SendMetadata{
					AuthorAccountID: actor.ID,
					ReplyToMsgID:    msg.ReplyToMsgID,
					RequiresAck:     msg.RequiresAck,
					Priority:        msg.Priority,
					ContentHash:     msg.ContentHash,
					MentionAll:      msg.MentionAll,
					MentionEveryone: msg.MentionEveryone,
				}); err != nil {
					return err
				}
				existing, gerr := b.Messages.Get(r.Context(), persistedID)
				if gerr != nil {
					return gerr
				}
				persisted = existing
			}

			members, err := b.Memberships.ListByRoom(r.Context(), roomID)
			if err != nil {
				return err
			}
			nowPtr := now
			for _, m := range members {
				st := &store.MessageState{
					MessageID: effectiveID,
					AccountID: m.AccountID,
				}
				if m.AccountID == actor.ID {
					st.ReadAt = &nowPtr
				}
				if err := b.MessageStates.Upsert(r.Context(), st); err != nil {
					return err
				}
			}
			// Author may not be a member (admin override path). Always
			// write the author's own state with read_at = now so the
			// state UI knows the author has seen their own message.
			if err := b.MessageStates.Upsert(r.Context(), &store.MessageState{
				MessageID: effectiveID,
				AccountID: actor.ID,
				ReadAt:    &nowPtr,
			}); err != nil {
				return err
			}
			// M7: persist attachment rows. local_path = the source
			// path the caller passed (already on the daemon's
			// filesystem); downloaded_at = now (no fetch needed for
			// outbound). discord_url comes from the Provider response
			// — Discord assigns a CDN URL after upload completes, so
			// downstream consumers can re-fetch via the URL if the
			// local file is removed.
			if len(uploads) > 0 {
				nowAtt := time.Now().UTC()
				for i, u := range uploads {
					attID, idErr := uuid.NewV7()
					if idErr != nil {
						return errcode.Wrap(idErr, errcode.Internal, "uuidv7 for attachment")
					}
					var (
						discordURL string
						sentName   string
					)
					if i < len(sent.Attachments) {
						discordURL = sent.Attachments[i].URL
						sentName = sent.Attachments[i].Filename
					}
					fname := u.FileName
					if sentName != "" {
						fname = sentName
					}
					row := &store.Attachment{
						ID:           attID.String(),
						MessageID:    effectiveID,
						Filename:     fname,
						Size:         uploadSizes[i],
						MIME:         u.MIME,
						LocalPath:    u.Path,
						DiscordURL:   discordURL,
						DownloadedAt: &nowAtt,
						CreatedAt:    nowAtt,
					}
					if err := b.Attachments.Create(r.Context(), row); err != nil {
						return err
					}
				}
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionMessageSend, effectiveID, map[string]any{
					"room_id":          roomID,
					"discord_msg_id":   sent.ID,
					"requires_ack":     req.RequiresAck,
					"priority":         string(priority),
					"mention_all":      req.MentionAll,
					"attachments":      len(uploads),
					"race_with_ingest": !inserted,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Notify every member of the room — new message changes
		// everyone's state (unread, mentions, priority feeds). M5
		// debounce coalesces bursts.
		go publishRoomMembers(bus, bundler, roomID)
		// Hydrate attachments onto the response so the caller can see
		// the persisted rows (with discord_url) immediately.
		resp := MessageToResponse(persisted)
		if atts, herr := hydrateAttachments(r.Context(), bundler, persisted.ID); herr == nil {
			resp.Attachments = atts
		}
		WriteJSON(w, http.StatusCreated, resp)
	}
}

// hydrateAttachments fetches and DTO-converts the attachments for one
// message id. Errors collapse to an empty slice (the message itself
// already committed; missing attachments shouldn't fail the response).
func hydrateAttachments(ctx context.Context, bundler store.Bundler, messageID string) ([]AttachmentResponse, error) {
	var atts []*store.Attachment
	if err := bundler.WithTx(ctx, func(b store.Bundle) error {
		got, err := b.Attachments.ListByMessage(ctx, messageID)
		if err != nil {
			return err
		}
		atts = got
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]AttachmentResponse, 0, len(atts))
	for _, a := range atts {
		out = append(out, AttachmentToResponse(a))
	}
	return out, nil
}

// publishRoomMembers fetches the room's current members and calls
// bus.PublishMany on their account ids. Runs in a goroutine off the
// request path so the HTTP response isn't delayed.
//
// M8-Q-P1-015: tx errors are now logged (via slog.Default — the
// daemon's logger is the runtime default in production, and tests
// stay silent because they pipe slog through io.Discard). Without
// this, a concurrent room delete or DB outage between the handler
// commit and this goroutine produced "no fanout" with zero
// visibility.
func publishRoomMembers(bus *state.Bus, bundler store.Bundler, roomID string) {
	if bus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bundler.WithTx(ctx, func(b store.Bundle) error {
		ms, err := b.Memberships.ListByRoom(ctx, roomID)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(ms))
		for _, m := range ms {
			ids = append(ids, m.AccountID)
		}
		bus.PublishMany(ids)
		return nil
	}); err != nil {
		slog.Warn("publishRoomMembers failed",
			"room_id", roomID, "err", err.Error())
	}
}

// ListMessages handles GET /v1/rooms/{id}/messages?before=&limit=.
// Members of the room (and admins) can list.
func ListMessages(bundler store.Bundler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			parsed, err := strconv.Atoi(v)
			if err != nil || parsed < 0 {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"limit must be a non-negative integer"))
				return
			}
			limit = parsed
		}
		before := r.URL.Query().Get("before")
		var messages []*store.Message
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if _, err := b.Rooms.Get(r.Context(), roomID); err != nil {
				return err
			}
			a, err := b.Accounts.Get(r.Context(), actor.ID)
			if err != nil {
				return err
			}
			if a.Role != store.RoleAdmin {
				if _, err := b.Memberships.Get(r.Context(), actor.ID, roomID); err != nil {
					if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
						return errcode.New(errcode.PermDenied,
							"actor %s is not a member of room %s", actor.ID, roomID)
					}
					return err
				}
			}
			ms, err := b.Messages.List(r.Context(), store.MessageFilter{
				RoomID:   roomID,
				BeforeID: before,
				Limit:    limit,
			})
			if err != nil {
				return err
			}
			messages = ms
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}
		// M7: batch-load attachments for all returned messages in one
		// round-trip to keep ListMessages O(1) DB calls regardless
		// of attachment count.
		ids := make([]string, 0, len(messages))
		for _, m := range messages {
			ids = append(ids, m.ID)
		}
		var attByMsg map[string][]*store.Attachment
		if len(ids) > 0 {
			_ = bundler.WithTx(r.Context(), func(b store.Bundle) error {
				m, err := b.Attachments.ListByMessages(r.Context(), ids)
				if err != nil {
					return err
				}
				attByMsg = m
				return nil
			})
		}

		out := MessageListResponse{Messages: make([]MessageResponse, 0, len(messages))}
		for _, m := range messages {
			resp := MessageToResponse(m)
			if rows, ok := attByMsg[m.ID]; ok && len(rows) > 0 {
				resp.Attachments = make([]AttachmentResponse, 0, len(rows))
				for _, a := range rows {
					resp.Attachments = append(resp.Attachments, AttachmentToResponse(a))
				}
			}
			out.Messages = append(out.Messages, resp)
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// MarkRead handles POST /v1/messages/{id}/read.
func MarkRead(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return mutateMessageState(bundler, recorder, bus, audit.ActionMessageRead,
		func(now time.Time, s *store.MessageState) { s.ReadAt = &now })
}

// ReplyAck handles POST /v1/messages/{id}/reply-ack.
func ReplyAck(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return mutateMessageState(bundler, recorder, bus, audit.ActionMessageReplyAck,
		func(now time.Time, s *store.MessageState) { s.RepliedAt = &now })
}

// mutateMessageState is the shared body for MarkRead and ReplyAck: the
// actor sets one of the two timestamps on their own MessageState row
// (which may not yet exist).
func mutateMessageState(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus,
	action audit.Action,
	patch func(now time.Time, s *store.MessageState),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		messageID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var out *store.MessageState
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			msg, err := b.Messages.Get(r.Context(), messageID)
			if err != nil {
				// M8-S-P2-009: collapse NotFound into PermDenied so a
				// caller cannot enumerate message ids by status-code
				// timing. UUIDv7 ids are time-sortable, which would
				// otherwise let an attacker who learned one valid id
				// fingerprint neighbouring ids' activity.
				if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
					return errcode.New(errcode.PermDenied,
						"actor %s cannot access message %s", actor.ID, messageID)
				}
				return err
			}
			if _, err := b.Memberships.Get(r.Context(), actor.ID, msg.RoomID); err != nil {
				if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
					return errcode.New(errcode.PermDenied,
						"actor %s is not a member of room %s", actor.ID, msg.RoomID)
				}
				return err
			}
			now := time.Now().UTC()
			state := &store.MessageState{
				MessageID: messageID,
				AccountID: actor.ID,
			}
			patch(now, state)
			if err := b.MessageStates.Upsert(r.Context(), state); err != nil {
				return err
			}
			out = state
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				action, messageID, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Only the actor's own state changed; publish just them.
		bus.Publish(out.AccountID)
		WriteJSON(w, http.StatusOK, MessageStateToResponse(out))
	}
}
