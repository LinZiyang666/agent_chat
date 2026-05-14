package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// SendMessage handles POST /v1/rooms/{id}/messages.
//
// Sequence:
//  1. Validate input (priority known; content non-empty).
//  2. Acquire actor's Provider (must be online).
//  3. Read tx: resolve room (must not be archived); read actor's role;
//     RoleUser must be a member of the room (PERM_DENIED otherwise),
//     RoleAdmin bypasses the membership gate per requirements §5.1
//     "发：admin 不受限" (M4-P3-004). Resolve reply parent if any.
//  4. Call Provider.SendMessage (slow, outside tx).
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
func SendMessage(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		var req SendMessageRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if req.Content == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "content is empty"))
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
		p, err := providerForActor(conn, actor.ID)
		if err != nil {
			WriteError(w, err)
			return
		}

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

		// Slow: Discord send. Outside tx.
		sent, err := p.SendMessage(r.Context(), room.DiscordChannelID, req.Content, bot.SendOptions{
			ReplyToMessageID: replyDiscord,
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
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionMessageSend, effectiveID, map[string]any{
					"room_id":          roomID,
					"discord_msg_id":   sent.ID,
					"requires_ack":     req.RequiresAck,
					"priority":         string(priority),
					"race_with_ingest": !inserted,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, MessageToResponse(persisted))
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
		out := MessageListResponse{Messages: make([]MessageResponse, 0, len(messages))}
		for _, m := range messages {
			out.Messages = append(out.Messages, MessageToResponse(m))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// MarkRead handles POST /v1/messages/{id}/read.
func MarkRead(bundler store.Bundler, recorder *audit.Recorder) http.HandlerFunc {
	return mutateMessageState(bundler, recorder, audit.ActionMessageRead,
		func(now time.Time, s *store.MessageState) { s.ReadAt = &now })
}

// ReplyAck handles POST /v1/messages/{id}/reply-ack.
func ReplyAck(bundler store.Bundler, recorder *audit.Recorder) http.HandlerFunc {
	return mutateMessageState(bundler, recorder, audit.ActionMessageReplyAck,
		func(now time.Time, s *store.MessageState) { s.RepliedAt = &now })
}

// mutateMessageState is the shared body for MarkRead and ReplyAck: the
// actor sets one of the two timestamps on their own MessageState row
// (which may not yet exist).
func mutateMessageState(bundler store.Bundler, recorder *audit.Recorder,
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
		WriteJSON(w, http.StatusOK, MessageStateToResponse(out))
	}
}
