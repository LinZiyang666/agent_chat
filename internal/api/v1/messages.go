package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
		// view. Also collect the room's current member set + each
		// member's bot identity here so the M9 Phase 2 outbound
		// mention parser runs against a consistent snapshot.
		//
		// Membership gate (M4-P3-004 fix): per requirements §5.1
		// "发：admin 不受限；user 只能在自己所属的群里发". Admins are
		// NOT required to be a member of the room. Real Discord
		// permission failures still surface from Provider.SendMessage.
		var (
			room         *store.Room
			replyDiscord string
			replyParent  *store.Message
			roomMembers  []bot.RoomMember
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
				// Non-admin callers may not impersonate system
				// traffic. The check is on the argument shape (an
				// admin-only value of the priority enum), so it is
				// reported as INVALID_ARGUMENT to match the documented
				// error-code matrix in USAGE.
				if priority == store.PrioritySystem {
					return errcode.New(errcode.InvalidArgument,
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
			// M9 Phase 2: assemble RoomMember snapshot for the
			// outbound mention parser. List memberships (account_id
			// only), then one Accounts.List walk to attach Name +
			// BotUserID. Account count is small (Discord caps
			// individual developers at ~10 applications, D3.1) so
			// the linear scan is fine.
			memberships, err := b.Memberships.ListByRoom(r.Context(), roomID)
			if err != nil {
				return err
			}
			if len(memberships) == 0 {
				return nil
			}
			accs, err := b.Accounts.List(r.Context())
			if err != nil {
				return err
			}
			byID := make(map[string]*store.Account, len(accs))
			for _, ac := range accs {
				byID[ac.ID] = ac
			}
			roomMembers = make([]bot.RoomMember, 0, len(memberships))
			for _, m := range memberships {
				ac, ok := byID[m.AccountID]
				if !ok {
					continue
				}
				roomMembers = append(roomMembers, bot.RoomMember{
					AccountID: ac.ID,
					Name:      ac.Name,
					BotUserID: ac.BotUserID,
				})
			}
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}

		// M9 Phase 2: parse outbound @-mentions BEFORE handing off to
		// Discord. The rewritten content (with `@<name>` substituted
		// for `<@bot_user_id>`) is what we send AND what we persist;
		// the allow-list slices feed Discord's AllowedMentions so
		// only resolved targets actually get pinged.
		parsed, err := bot.ParseMentions(req.Content, roomMembers)
		if err != nil {
			WriteError(w, err)
			return
		}
		// M9 Phase 2: the only source of @everyone is the outbound
		// parser. The legacy `--all` / `req.MentionAll` flag was
		// removed in this milestone.
		allowEveryone := parsed.Everyone

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
		sent, err := p.SendMessage(r.Context(), room.DiscordChannelID, parsed.RewrittenContent, bot.SendOptions{
			ReplyToMessageID:       replyDiscord,
			Attachments:            uploads,
			MentionAllowedUserIDs:  parsed.BotUserIDs,
			MentionAllowedEveryone: allowEveryone,
		})
		if err != nil {
			WriteError(w, err)
			return
		}

		// Persist + fan-out states. We store the REWRITTEN content
		// (with `<@bot_user_id>` substitutions) so history readers
		// see the same form Discord rendered, and content_hash hashes
		// what we actually stored.
		msgID, err := uuid.NewV7()
		if err != nil {
			WriteError(w, errcode.Wrap(err, errcode.Internal, "uuidv7"))
			return
		}
		hash := sha256.Sum256([]byte(parsed.RewrittenContent))
		now := time.Now().UTC()
		msg := &store.Message{
			ID:              msgID.String(),
			RoomID:          roomID,
			AuthorAccountID: actor.ID,
			DiscordMsgID:    sent.ID,
			Content:         parsed.RewrittenContent,
			Priority:        priority,
			CreatedAt:       sent.CreatedAt.UTC(),
			ContentHash:     hex.EncodeToString(hash[:]),
			// M9 Phase 2: mention_everyone is sourced from the
			// outbound parser. requires_ack / mention_all retired.
			MentionEveryone: allowEveryone,
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
					Priority:        msg.Priority,
					ContentHash:     msg.ContentHash,
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
			// M9 Phase 2: persist per-account mention rows resolved by
			// the outbound parser. AddForMessage uses INSERT OR IGNORE
			// so a later ingester echo merging in the same accounts is
			// idempotent — and the conflict path is safe in either
			// direction (P2-1 review fix).
			if len(parsed.MentionedAccountIDs) > 0 {
				if err := b.MessageMentions.AddForMessage(
					r.Context(), effectiveID, parsed.MentionedAccountIDs); err != nil {
					return err
				}
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
			//
			// Race with ingester: if the gateway echo landed before
			// this send tx (inserted=false), the ingester already
			// inserted one attachment row per Discord attachment with
			// LocalPath empty. Creating a second row here would yield
			// the "two rows per file" bug seen in M9 audit (read
			// returns both a discord-only entry and a local-only
			// entry). Detect that case and patch the existing rows
			// via MarkDownloaded instead.
			if len(uploads) > 0 {
				nowAtt := time.Now().UTC()
				var existing []*store.Attachment
				if !inserted {
					ex, err := b.Attachments.ListByMessage(r.Context(), effectiveID)
					if err != nil {
						return err
					}
					existing = ex
				}
				for i, u := range uploads {
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
					if i < len(existing) {
						// Patch ingester's placeholder row with the
						// local path / downloaded_at the send path
						// owns. sha256 is left empty; outbound
						// dedupe by hash is not required.
						if err := b.Attachments.MarkDownloaded(
							r.Context(), existing[i].ID, u.Path, "", nowAtt); err != nil {
							return err
						}
						continue
					}
					attID, idErr := uuid.NewV7()
					if idErr != nil {
						return errcode.Wrap(idErr, errcode.Internal, "uuidv7 for attachment")
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
					"priority":         string(priority),
					"mention_everyone": allowEveryone,
					"mentions":         len(parsed.MentionedAccountIDs),
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

// rawMentionRenderRe matches the Discord-native `<@id>` mention syntax
// produced by the outbound parser. The id character class is
// deliberately loose ([\w-]+) so mock fixtures using non-numeric IDs
// like `<@u-alice>` are rendered the same way real Discord snowflakes
// are. `<#id>` (channel) and `<@&roleid>` (role) are excluded —
// agentchat doesn't model those.
var rawMentionRenderRe = regexp.MustCompile(`<@!?([\w-]+)>`)

// renderDisplayContent replaces every `<@bot_user_id>` token in
// content with `@<name>` if the id maps to a known agentchat
// account; unknown ids are preserved verbatim. Used by ReadRoom to
// populate MessageResponse.DisplayContent (M9 Phase 2).
func renderDisplayContent(content string, nameByBotUserID map[string]string) string {
	if content == "" || len(nameByBotUserID) == 0 {
		return content
	}
	return rawMentionRenderRe.ReplaceAllStringFunc(content, func(match string) string {
		// Strip the surrounding `<@` / `<@!` and `>` to get the id.
		idStart := 2
		if len(match) > 2 && match[2] == '!' {
			idStart = 3
		}
		id := match[idStart : len(match)-1]
		if name, ok := nameByBotUserID[id]; ok && name != "" {
			return "@" + name
		}
		return match
	})
}

// ReadRoom handles POST /v1/rooms/{id}/read (M9 Phase 2).
//
// Default mode (no `before`):
//  1. Authorize: actor must be a member of the room (admins OK; same
//     gate as SendMessage). NotFound on the room is folded into
//     PERM_DENIED so id enumeration via response code is shut down,
//     matching the M8-S-P2-009 hardening on per-message routes.
//  2. List unread messages for the actor in this room (cap 200) and
//     read-history (cap = req.Limit, default 10) in one tx.
//  3. UPSERT message_states.read_at = now for every unread row.
//  4. publish(actor) once after commit so the aggregator recomputes
//     state.totals.unread / mentions / priority.
//
// `before` mode: skips step 3 entirely (pure history paging) and uses
// MessageRepo.List(BeforeID) so the caller can scroll back without
// touching read state. This is the only legal way to read messages
// older than what the unread/context window returned.
func ReadRoom(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		// Body is optional. An empty body decodes into the zero
		// ReadRoomRequest, which yields the defaults.
		var req ReadRoomRequest
		if r.ContentLength > 0 {
			if err := DecodeJSON(r, &req); err != nil {
				WriteError(w, err)
				return
			}
		}
		if req.Limit < 0 {
			WriteError(w, errcode.New(errcode.InvalidArgument, "limit must be non-negative"))
			return
		}
		// Defaults follow docs/06-cli-redesign.md §3.3:
		//   - default mode (no --before): 10 context messages
		//   - --before pagination:          50 history messages
		// Both modes share a hard ceiling of 200.
		ctxLimit := req.Limit
		if ctxLimit == 0 {
			if req.Before != "" {
				ctxLimit = 50
			} else {
				ctxLimit = 10
			}
		}
		if ctxLimit > 200 {
			ctxLimit = 200
		}

		var (
			roomRow    *store.Room
			subscribed bool
			messages   []*store.Message
			markedRead []string
			more       bool
		)
		now := time.Now().UTC()

		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			rr, err := b.Rooms.Get(r.Context(), roomID)
			if err != nil {
				if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
					return errcode.New(errcode.NotFound,
						"room %s not found", roomID)
				}
				return err
			}
			roomRow = rr
			a, err := b.Accounts.Get(r.Context(), actor.ID)
			if err != nil {
				return err
			}
			if a.Role != store.RoleAdmin {
				mb, err := b.Memberships.Get(r.Context(), actor.ID, roomID)
				if err != nil {
					if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
						return errcode.New(errcode.PermDenied,
							"actor %s is not a member of room %s", actor.ID, roomID)
					}
					return err
				}
				subscribed = mb.Subscribed
			} else {
				// Admins can read any room; subscribed is "yes" if they
				// have an explicit membership row (which not all admins
				// do — system admins frequently use the override path).
				if mb, err := b.Memberships.Get(r.Context(), actor.ID, roomID); err == nil {
					subscribed = mb.Subscribed
				}
			}

			if req.Before != "" {
				// Pure-query path: history page before a given id,
				// no read-state mutation.
				rows, err := b.Messages.List(r.Context(), store.MessageFilter{
					RoomID:   roomID,
					BeforeID: req.Before,
					Limit:    ctxLimit,
				})
				if err != nil {
					return err
				}
				messages = rows
				more = len(rows) == ctxLimit
				return nil
			}

			// Default path: unread (cap 200) + read history (ctxLimit).
			unread, err := b.Messages.ListUnreadForAccountInRoom(r.Context(), actor.ID, roomID, 200)
			if err != nil {
				return err
			}
			ctx, err := b.Messages.ListReadHistoryForAccountInRoom(r.Context(), actor.ID, roomID, ctxLimit)
			if err != nil {
				return err
			}
			// Mark unread as read in the same tx so observers can't
			// see an intermediate state.
			markedRead = make([]string, 0, len(unread))
			for _, m := range unread {
				st := &store.MessageState{
					MessageID: m.ID,
					AccountID: actor.ID,
					ReadAt:    &now,
				}
				if err := b.MessageStates.Upsert(r.Context(), st); err != nil {
					return err
				}
				markedRead = append(markedRead, m.ID)
				if err := recorder.RecordVia(r.Context(), b.Audit, actor.ID,
					audit.ActionMessageRead, m.ID, nil); err != nil {
					return err
				}
			}
			// Combine and sort oldest -> newest. Both source lists are
			// newest-first; reverse them while concatenating.
			messages = make([]*store.Message, 0, len(unread)+len(ctx))
			for i := len(ctx) - 1; i >= 0; i-- {
				messages = append(messages, ctx[i])
			}
			for i := len(unread) - 1; i >= 0; i-- {
				messages = append(messages, unread[i])
			}
			// "more" hint: unread feed was capped → caller may need
			// to call read again (or use --before) to drain the rest.
			more = len(unread) == 200
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}

		// Hydrate the response. We need the room-level pieces
		// (current_announcement_id, the account-name lookup tables)
		// unconditionally — they describe the room itself, not the
		// returned message slice — so the tx runs even when
		// `messages` is empty (a freshly-created room or a viewer
		// that has read everything still needs an accurate
		// `room.current_announcement_id`, P2-a review fix).
		ids := make([]string, 0, len(messages))
		for _, m := range messages {
			ids = append(ids, m.ID)
		}
		var (
			attByMsg      map[string][]*store.Attachment
			mentionsByID  = map[string][]string{}
			nameByAccount = map[string]string{}
			nameByBotUser = map[string]string{}
			readAtByMsg   = map[string]time.Time{}
			currentAnnID  string
		)
		_ = bundler.WithTx(r.Context(), func(b store.Bundle) error {
			// Per-message hydration — skipped if there are no
			// messages to enrich.
			if len(ids) > 0 {
				if rows, err := b.Attachments.ListByMessages(r.Context(), ids); err == nil {
					attByMsg = rows
				}
				for _, id := range ids {
					accs, err := b.MessageMentions.ListForMessage(r.Context(), id)
					if err != nil {
						continue
					}
					if len(accs) > 0 {
						mentionsByID[id] = accs
					}
				}
				// Per-account read timestamps for THIS caller, so the
				// response can carry read_at per row (designed in
				// docs/06-cli-redesign.md §3.4). One Get per message
				// is acceptable at our scale (cap 200); a bulk
				// variant can replace this if profiles flag it.
				for _, id := range ids {
					st, err := b.MessageStates.Get(r.Context(), id, actor.ID)
					if err != nil || st == nil || st.ReadAt == nil {
						continue
					}
					readAtByMsg[id] = *st.ReadAt
				}
			}
			// Room-level hydration: always run.
			// Build account.id / bot_user_id -> name maps. The
			// account list is small (Discord caps individual devs
			// at ~10 applications) so List + linear-scan is fine.
			if accounts, err := b.Accounts.List(r.Context()); err == nil {
				for _, a := range accounts {
					nameByAccount[a.ID] = a.Name
					if a.BotUserID != "" {
						nameByBotUser[a.BotUserID] = a.Name
					}
				}
			}
			// Current room announcement id (latest version, if any).
			if ann, err := b.Announcements.Latest(r.Context(), roomRow.ID); err == nil && ann != nil {
				currentAnnID = ann.ID
			}
			return nil
		})

		out := ReadRoomResponse{
			Room: ReadRoomRoom{
				ID:                    roomRow.ID,
				Name:                  roomRow.Name,
				Subscribed:            subscribed,
				CurrentAnnouncementID: currentAnnID,
			},
			MarkedRead: markedRead,
			Messages:   make([]MessageResponse, 0, len(messages)),
			More:       more,
		}
		if markedRead == nil {
			out.MarkedRead = []string{}
		}
		for _, m := range messages {
			resp := MessageToResponse(m)
			resp.AuthorName = nameByAccount[m.AuthorAccountID]
			resp.DisplayContent = renderDisplayContent(m.Content, nameByBotUser)
			if t, ok := readAtByMsg[m.ID]; ok {
				v := t.UTC()
				resp.ReadAt = &v
			}
			if rows, ok := attByMsg[m.ID]; ok && len(rows) > 0 {
				resp.Attachments = make([]AttachmentResponse, 0, len(rows))
				for _, a := range rows {
					resp.Attachments = append(resp.Attachments, AttachmentToResponse(a))
				}
			}
			if accs, ok := mentionsByID[m.ID]; ok {
				resp.Mentions = accs
			}
			out.Messages = append(out.Messages, resp)
		}

		// Publish AFTER the read tx commits so watchers don't see a
		// rebuild based on uncommitted state. Skip when no read
		// happened (--before path) to avoid spurious snapshot rebuilds.
		if len(markedRead) > 0 {
			bus.Publish(actor.ID)
		}
		WriteJSON(w, http.StatusOK, out)
	}
}
