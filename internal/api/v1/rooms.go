package v1

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/account"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/state"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// validateRoomName mirrors validateName in internal/account but allows
// names up to Discord's 100-char channel limit. Lower-cased + dashed
// is NOT enforced here — Discord will sanitise on its end and we keep
// our own canonical name field for display.
func validateRoomName(name string) error {
	if name == "" {
		return errcode.New(errcode.InvalidArgument, "name is empty")
	}
	if strings.TrimSpace(name) != name {
		return errcode.New(errcode.InvalidArgument, "name has leading/trailing whitespace")
	}
	if len(name) > 100 {
		return errcode.New(errcode.InvalidArgument, "name is longer than 100 bytes")
	}
	return nil
}

// providerForActor returns the actor account's online Provider. The
// caller-supplied actor must have an online Provider — this matches
// the M3-P3-001/002 pattern: any operation that uses the bot requires
// the actor to be online so we don't dispatch on stale state.
func providerForActor(conn *connector.Connector, actorID string) (bot.Provider, error) {
	p, ok := conn.Provider(actorID)
	if !ok {
		return nil, errcode.New(errcode.Conflict,
			"actor account %s has no live Discord provider; call 'admin account online' first",
			actorID)
	}
	return p, nil
}

// CreateRoom handles POST /v1/rooms.
//
// Sequence:
//  1. Acquire the actor's live Provider.
//  2. Call Provider.CreateChannel (slow; outside any tx).
//  3. In a transaction: insert the room row, insert a creator
//     membership with subscribed=false (admin creates the room but
//     does not auto-subscribe; M4 demo follows roadmap §5 conventions),
//     and write the room.create audit row.
//  4. On tx failure best-effort delete the just-created Discord channel.
func CreateRoom(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateRoomRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if err := validateRoomName(req.Name); err != nil {
			WriteError(w, err)
			return
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
		channelID, err := p.CreateChannel(r.Context(), req.Name)
		if err != nil {
			WriteError(w, err)
			return
		}
		var created *store.Room
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			id, err := uuid.NewV7()
			if err != nil {
				return errcode.Wrap(err, errcode.Internal, "uuidv7")
			}
			now := time.Now().UTC()
			room := &store.Room{
				ID:               id.String(),
				DiscordChannelID: channelID,
				Name:             req.Name,
				Archived:         false,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if err := b.Rooms.Create(r.Context(), room); err != nil {
				return err
			}
			// Admin who created the room joins (unsubscribed by
			// default — they're typically not the room's audience).
			if err := b.Memberships.Upsert(r.Context(), &store.Membership{
				AccountID:  actor.ID,
				RoomID:     room.ID,
				Subscribed: false,
				JoinedAt:   now,
			}); err != nil {
				return err
			}
			created = room
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionRoomCreate, room.ID, map[string]any{
					"name":               req.Name,
					"discord_channel_id": channelID,
				})
		}); err != nil {
			_ = p.DeleteChannel(context.Background(), channelID)
			WriteError(w, err)
			return
		}
		// Creator is the only initial member; publish to them.
		bus.Publish(actor.ID)
		WriteJSON(w, http.StatusCreated, RoomToResponse(created))
	}
}

// ListRooms handles GET /v1/rooms.
//
// Admin sees every room; user sees only the rooms they are a member
// of. Archived rooms are included only when ?include_archived=true.
func ListRooms(svc *account.Service, bundler store.Bundler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		includeArchived := r.URL.Query().Get("include_archived") == "true"
		var rooms []*store.Room
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a, err := b.Accounts.Get(r.Context(), actor.ID)
			if err != nil {
				return err
			}
			if a.Role == store.RoleAdmin {
				rooms, err = b.Rooms.List(r.Context(), includeArchived)
			} else {
				rooms, err = b.Rooms.ListByMember(r.Context(), actor.ID, includeArchived)
			}
			return err
		}); err != nil {
			WriteError(w, err)
			return
		}
		out := RoomListResponse{Rooms: make([]RoomResponse, 0, len(rooms))}
		for _, rr := range rooms {
			out.Rooms = append(out.Rooms, RoomToResponse(rr))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// GetRoom handles GET /v1/rooms/{id}. Admins always allowed; users
// must be members of the room.
func GetRoom(bundler store.Bundler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var room *store.Room
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			rr, err := b.Rooms.Get(r.Context(), id)
			if err != nil {
				return err
			}
			a, err := b.Accounts.Get(r.Context(), actor.ID)
			if err != nil {
				return err
			}
			if a.Role != store.RoleAdmin {
				if _, err := b.Memberships.Get(r.Context(), actor.ID, id); err != nil {
					return err
				}
			}
			room = rr
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, RoomToResponse(room))
	}
}

// UpdateRoom handles PATCH /v1/rooms/{id}. Admin only. Name is the
// only mutable field today; the channel-side rename is best-effort
// (Discord channel-name retains discord-side validation rules).
func UpdateRoom(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req UpdateRoomRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		if req.Name == nil {
			WriteError(w, errcode.New(errcode.InvalidArgument, "no fields to update"))
			return
		}
		if err := validateRoomName(*req.Name); err != nil {
			WriteError(w, err)
			return
		}
		var updated *store.Room
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			room, err := b.Rooms.Get(r.Context(), id)
			if err != nil {
				return err
			}
			room.Name = *req.Name
			room.UpdatedAt = time.Now().UTC()
			if err := b.Rooms.Update(r.Context(), room); err != nil {
				return err
			}
			updated = room
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionRoomRename, room.ID, map[string]any{"name": room.Name})
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Rename touches every member's room name in their state.
		go publishRoomMembers(bus, bundler, id)
		WriteJSON(w, http.StatusOK, RoomToResponse(updated))
	}
}

// ArchiveRoom handles POST /v1/rooms/{id}/archive. Sets archived = 1
// but keeps the Discord channel and the DB row intact (history
// survives).
func ArchiveRoom(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var updated *store.Room
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			room, err := b.Rooms.Get(r.Context(), id)
			if err != nil {
				return err
			}
			room.Archived = true
			room.UpdatedAt = time.Now().UTC()
			if err := b.Rooms.Update(r.Context(), room); err != nil {
				return err
			}
			updated = room
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionRoomArchive, room.ID, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Archive affects everyone whose state listed this room.
		go publishRoomMembers(bus, bundler, id)
		WriteJSON(w, http.StatusOK, RoomToResponse(updated))
	}
}

// DeleteRoom handles DELETE /v1/rooms/{id}.
//
// Ordering (M4-P3-001 fix): the Discord channel deletion MUST succeed
// before we drop the local row + audit. Previously we deleted the DB
// row first and swallowed Discord errors, which left orphan channels
// when the API call returned 204. Now:
//
//  1. Resolve the room (need its discord_channel_id) and require the
//     actor's Provider to be online.
//  2. Call Provider.DeleteChannel — slow, outside any tx. If Discord
//     returns an error, propagate it and leave the DB row in place so
//     a retry resolves it.
//  3. Only then enter WithTx: delete the row + write the audit.
//
// The schema's ON DELETE CASCADE clears memberships, messages, and
// message_states for free when the row is finally deleted.
func DeleteRoom(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
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
		// Snapshot the member list BEFORE delete cascades it. Used
		// for the post-commit state publish.
		var (
			channelID string
			memberIDs []string
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			room, err := b.Rooms.Get(r.Context(), id)
			if err != nil {
				return err
			}
			channelID = room.DiscordChannelID
			members, err := b.Memberships.ListByRoom(r.Context(), id)
			if err != nil {
				return err
			}
			for _, m := range members {
				memberIDs = append(memberIDs, m.AccountID)
			}
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}
		if channelID != "" {
			if err := p.DeleteChannel(r.Context(), channelID); err != nil {
				WriteError(w, err)
				return
			}
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if err := b.Rooms.Delete(r.Context(), id); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionRoomDelete, id, map[string]any{
					"discord_channel_id": channelID,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		bus.PublishMany(memberIDs)
		w.WriteHeader(http.StatusNoContent)
	}
}

// InviteMember handles POST /v1/rooms/{id}/members. Admin only.
//
// Ordering (M4-P3-003 fix): previously we Upsert'd the membership and
// wrote the audit FIRST, then called AddMember; if the Discord grant
// failed we unconditionally Deleted the row — which could wipe a
// pre-existing membership (e.g., the admin invited the same account
// again with subscribed=true after a prior subscribed=false). Now:
//
//  1. Read tx: resolve the room, capture the target's bot_user_id and
//     the prior membership (if any) so we can detect a fresh insert
//     vs an update.
//  2. Call Provider.AddMember — slow, outside any tx. On error, do
//     NOT touch the DB at all; the prior membership state is left
//     intact, and no audit row is written.
//  3. Write tx: Upsert the membership (subscribed per request) +
//     write the audit row.
//
// Target's account must exist AND have come online once so we have a
// bot_user_id to grant the channel permission override.
func InviteMember(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		var req InviteRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if req.AccountID == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "account_id is empty"))
			return
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
		var (
			room      *store.Room
			targetUID string
			prior     *store.Membership
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			rr, err := b.Rooms.Get(r.Context(), roomID)
			if err != nil {
				return err
			}
			room = rr
			target, err := b.Accounts.Get(r.Context(), req.AccountID)
			if err != nil {
				return err
			}
			if target.BotUserID == "" {
				return errcode.New(errcode.InvalidArgument,
					"target account %s has no captured Discord identity; bring it online once first",
					req.AccountID)
			}
			targetUID = target.BotUserID
			existing, err := b.Memberships.Get(r.Context(), req.AccountID, roomID)
			if err != nil {
				if ec, _ := errcode.As(err); ec == nil || ec.Code != errcode.NotFound {
					return err
				}
				// No prior membership — that's fine.
				return nil
			}
			prior = existing
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}

		if err := p.AddMember(r.Context(), room.DiscordChannelID, targetUID); err != nil {
			WriteError(w, err)
			return
		}

		now := time.Now().UTC()
		joinedAt := now
		if prior != nil {
			// Preserve the original join timestamp on re-invite.
			joinedAt = prior.JoinedAt
		}
		membership := &store.Membership{
			AccountID:  req.AccountID,
			RoomID:     roomID,
			Subscribed: req.Subscribed,
			JoinedAt:   joinedAt,
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if err := b.Memberships.Upsert(r.Context(), membership); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionRoomMemberAdd, roomID, map[string]any{
					"account_id":   req.AccountID,
					"subscribed":   req.Subscribed,
					"prior_member": prior != nil,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Only publish to the target when the new membership is
		// subscribed (M5-P3-003 fix). Unsubscribed memberships are
		// secondary-state (旁观) per requirements §4 / §5.2.1 and
		// must not push to the target's primary watch stream. The
		// target will see the room appear once they call
		// PATCH /v1/memberships/{room_id} to subscribe — that path
		// already publishes via UpdateMembership.
		if req.Subscribed {
			bus.Publish(req.AccountID)
		}
		WriteJSON(w, http.StatusCreated, MembershipToResponse(membership))
	}
}

// KickMember handles DELETE /v1/rooms/{id}/members/{account_id}.
// Admin only.
//
// Ordering (M4-P3-002 fix): the Discord per-channel permission delete
// MUST succeed before we drop the local membership + audit. Previously
// we removed the membership first and swallowed Discord errors,
// producing a split where agentchat said "kicked" but the Discord bot
// still had VIEW_CHANNEL. Now:
//
//  1. Resolve the room + target's bot_user_id.
//  2. Call Provider.RemoveMember (slow, outside tx) when there is a
//     captured bot_user_id. If Discord returns an error, propagate it
//     and leave the membership in place.
//  3. Only then enter WithTx: delete the membership + write the
//     audit.
//
// A target whose bot_user_id was never captured (never came online)
// has no Discord-side override to remove — that case skips step 2.
func KickMember(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		targetID := chi.URLParam(r, "account_id")
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
		var (
			room      *store.Room
			targetUID string
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			rr, err := b.Rooms.Get(r.Context(), roomID)
			if err != nil {
				return err
			}
			room = rr
			target, err := b.Accounts.Get(r.Context(), targetID)
			if err != nil {
				return err
			}
			targetUID = target.BotUserID
			if _, err := b.Memberships.Get(r.Context(), targetID, roomID); err != nil {
				return err
			}
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}
		if targetUID != "" {
			if err := p.RemoveMember(r.Context(), room.DiscordChannelID, targetUID); err != nil {
				WriteError(w, err)
				return
			}
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if err := b.Memberships.Delete(r.Context(), targetID, roomID); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionRoomMemberRemove, roomID, map[string]any{
					"account_id": targetID,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Target lost a membership → their state changes.
		bus.Publish(targetID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ListMembers handles GET /v1/rooms/{id}/members. Admins and members
// of the room can list.
func ListMembers(bundler store.Bundler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var members []*store.Membership
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
					return err
				}
			}
			ms, err := b.Memberships.ListByRoom(r.Context(), roomID)
			if err != nil {
				return err
			}
			members = ms
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}
		out := MembershipListResponse{Members: make([]MembershipResponse, 0, len(members))}
		for _, m := range members {
			out.Members = append(out.Members, MembershipToResponse(m))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// UpdateMembership handles PATCH /v1/memberships/{room_id}. Available
// to authenticated users; lets a member toggle their own subscribed
// flag (requirements §4 rule 4).
func UpdateMembership(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "room_id")
		var req UpdateMembershipRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var updated *store.Membership
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			m, err := b.Memberships.Get(r.Context(), actor.ID, roomID)
			if err != nil {
				return err
			}
			m.Subscribed = req.Subscribed
			if err := b.Memberships.SetSubscribed(r.Context(), actor.ID, roomID, req.Subscribed); err != nil {
				return err
			}
			updated = m
			action := audit.ActionMembershipUnsubscribe
			if req.Subscribed {
				action = audit.ActionMembershipSubscribe
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				action, roomID, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Actor's subscribed flag changed → their primary/secondary
		// state UI partition shifts.
		bus.Publish(actor.ID)
		WriteJSON(w, http.StatusOK, MembershipToResponse(updated))
	}
}
