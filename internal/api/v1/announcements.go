package v1

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

// CreateAnnouncement handles POST /v1/rooms/{id}/announcement.
//
// Sequence:
//  1. WithTx: resolve room (not archived), check membership (any
//     member or admin), allocate version, insert announcement row,
//     audit.
//  2. After commit: post a *mirror message* to the same Discord
//     channel via the actor's Provider so that humans reading
//     Discord directly (who never look at agentchat state) still see
//     the announcement content. The mirror is **best-effort** — a
//     Provider failure here is logged but does NOT roll the
//     announcement back. Rationale: the agentchat-side announcement
//     row is the source of truth for the mandatory-read semantics
//     (counted in `Totals.Announcements`); the Discord mirror is a
//     courtesy to non-agentchat-aware viewers. If the mirror failed,
//     the announcer (or any admin) can re-mirror by running
//     `agentchat send <room> "[announcement v<N>] <content>"`
//     themselves.
//  3. publishRoomMembers so every member's state recomputes.
//
// The actor must be online (Provider acquired) for the mirror step;
// the membership check already ran in step 1. We deliberately keep
// the M5 publish on the success path even when the mirror fails so
// state observers still get the new-announcement frame.
func CreateAnnouncement(conn *connector.Connector, bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		var req CreateAnnouncementRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if req.Content == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "content is empty"))
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			WriteError(w, errcode.Wrap(err, errcode.Internal, "uuidv7"))
			return
		}
		var (
			persisted *store.Announcement
			channelID string
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			room, err := b.Rooms.Get(r.Context(), roomID)
			if err != nil {
				return err
			}
			if room.Archived {
				return errcode.New(errcode.Conflict, "room %s is archived", roomID)
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
			version, err := b.Announcements.NextVersion(r.Context(), roomID)
			if err != nil {
				return err
			}
			ann := &store.Announcement{
				ID:        id.String(),
				RoomID:    roomID,
				Content:   req.Content,
				Version:   version,
				CreatedBy: actor.ID,
				CreatedAt: time.Now().UTC(),
			}
			if err := b.Announcements.Create(r.Context(), ann); err != nil {
				return err
			}
			persisted = ann
			channelID = room.DiscordChannelID
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAnnouncementCreate, ann.ID, map[string]any{
					"room_id": roomID,
					"version": version,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Best-effort mirror to the Discord channel. Failures do NOT
		// fail the request — the agentchat announcement row is the
		// source of truth; Discord echo is UX-only.
		if conn != nil && channelID != "" {
			mirrorAnnouncementBestEffort(r.Context(), conn, actor.ID, channelID, persisted, log)
		}
		// New announcement → every member's unread count just changed.
		go publishRoomMembers(bus, bundler, roomID)
		WriteJSON(w, http.StatusCreated, AnnouncementToResponse(persisted))
	}
}

// mirrorAnnouncementBestEffort sends the announcement content as a
// regular message to the Discord channel. It is intentionally fire-
// and-forget at the agentchat layer: if the Provider call fails (bot
// offline, permission revoked, Discord 5xx), the announcement row
// stays in the DB and surfaces normally in agentchat state. Failures
// are logged at WARN so an operator can re-mirror by hand.
//
// The mirror content carries the version explicitly so a reader
// scrolling through #channel can tell which iteration they're seeing,
// matching the agentchat-side semantics where the latest version
// supersedes prior ones.
func mirrorAnnouncementBestEffort(ctx context.Context, conn *connector.Connector, actorID, channelID string, ann *store.Announcement, log *slog.Logger) {
	if conn == nil {
		return
	}
	p, err := providerForActor(conn, actorID)
	if err != nil {
		if log != nil {
			log.Warn("announcement mirror skipped — provider not available",
				"announcement_id", ann.ID, "actor_id", actorID, "err", err.Error())
		}
		return
	}
	body := fmt.Sprintf("📢 **公告 v%d**\n%s", ann.Version, ann.Content)
	if _, err := p.SendMessage(ctx, channelID, body, bot.SendOptions{}); err != nil {
		if log != nil {
			log.Warn("announcement mirror to Discord failed; announcement row still committed",
				"announcement_id", ann.ID, "channel_id", channelID, "err", err.Error())
		}
		return
	}
}

// GetAnnouncement handles GET /v1/rooms/{id}/announcement.
// Returns the latest version. The Read field reflects whether the
// caller has ack'd this specific version.
func GetAnnouncement(bundler store.Bundler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var (
			ann  *store.Announcement
			read bool
		)
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
			latest, err := b.Announcements.Latest(r.Context(), roomID)
			if err != nil {
				return err
			}
			ann = latest
			read, err = b.AnnouncementReads.IsRead(r.Context(), latest.ID, actor.ID)
			return err
		}); err != nil {
			WriteError(w, err)
			return
		}
		resp := AnnouncementToResponse(ann)
		resp.Read = read
		WriteJSON(w, http.StatusOK, resp)
	}
}

// MarkAnnouncementRead handles POST /v1/announcements/{id}/read.
//
// Anyone who is a member of the announcement's room (or an admin) can
// ack. Re-acks are idempotent — read_at is overwritten with the new
// timestamp.
func MarkAnnouncementRead(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		annID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var out *store.AnnouncementRead
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			ann, err := b.Announcements.Get(r.Context(), annID)
			if err != nil {
				// Surface NOT_FOUND for missing IDs to match the
				// documented error-code matrix. Announcement IDs are
				// uuidv7 (128 bits) so enumeration is not a realistic
				// threat; downstream membership check still gates
				// access to existing-but-foreign announcements.
				if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
					return errcode.New(errcode.NotFound,
						"announcement %s not found", annID)
				}
				return err
			}
			a, err := b.Accounts.Get(r.Context(), actor.ID)
			if err != nil {
				return err
			}
			if a.Role != store.RoleAdmin {
				if _, err := b.Memberships.Get(r.Context(), actor.ID, ann.RoomID); err != nil {
					if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
						return errcode.New(errcode.PermDenied,
							"actor %s is not a member of room %s", actor.ID, ann.RoomID)
					}
					return err
				}
			}
			now := time.Now().UTC()
			rec := &store.AnnouncementRead{
				AnnouncementID: annID,
				AccountID:      actor.ID,
				ReadAt:         now,
			}
			if err := b.AnnouncementReads.Upsert(r.Context(), rec); err != nil {
				return err
			}
			out = rec
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAnnouncementRead, annID, map[string]any{
					"room_id": ann.RoomID,
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Only the actor's own unread count changed.
		if bus != nil {
			bus.Publish(out.AccountID)
		}
		WriteJSON(w, http.StatusOK, AnnouncementReadResponse{
			AnnouncementID: out.AnnouncementID,
			AccountID:      out.AccountID,
			ReadAt:         out.ReadAt,
		})
	}
}

// CreateSystemAnnouncement handles POST /v1/system/announcements.
// Admin-only.
func CreateSystemAnnouncement(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateSystemAnnouncementRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if req.Content == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "content is empty"))
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			WriteError(w, errcode.Wrap(err, errcode.Internal, "uuidv7"))
			return
		}
		var (
			persisted *store.SystemAnnouncement
			everyone  []string
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			ann := &store.SystemAnnouncement{
				ID:        id.String(),
				Content:   req.Content,
				CreatedBy: actor.ID,
				CreatedAt: time.Now().UTC(),
			}
			if err := b.SystemAnnouncements.Create(r.Context(), ann); err != nil {
				return err
			}
			persisted = ann
			// Capture every account so we can publish state to them
			// after commit (their unread-system-announcement count just
			// changed from N to N+1).
			accs, err := b.Accounts.List(r.Context())
			if err != nil {
				return err
			}
			everyone = make([]string, 0, len(accs))
			for _, a := range accs {
				everyone = append(everyone, a.ID)
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionSystemAnnCreate, ann.ID, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		if bus != nil && len(everyone) > 0 {
			bus.PublishMany(everyone)
		}
		WriteJSON(w, http.StatusCreated, SystemAnnouncementToResponse(persisted))
	}
}

// ListSystemAnnouncements handles GET /v1/system/announcements.
// Any authenticated caller sees the list, with their per-row Read
// status filled in.
func ListSystemAnnouncements(bundler store.Bundler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var out SystemAnnouncementListResponse
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			anns, err := b.SystemAnnouncements.List(r.Context(), 0)
			if err != nil {
				return err
			}
			out.Announcements = make([]SystemAnnouncementResponse, 0, len(anns))
			for _, a := range anns {
				resp := SystemAnnouncementToResponse(a)
				read, err := b.SystemAnnouncementReads.IsRead(r.Context(), a.ID, actor.ID)
				if err != nil {
					return err
				}
				resp.Read = read
				out.Announcements = append(out.Announcements, resp)
			}
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// MarkSystemAnnouncementRead handles POST /v1/system/announcements/{id}/read.
func MarkSystemAnnouncementRead(bundler store.Bundler, recorder *audit.Recorder, bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		annID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var out *store.SystemAnnouncementRead
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if _, err := b.SystemAnnouncements.Get(r.Context(), annID); err != nil {
				return err
			}
			now := time.Now().UTC()
			rec := &store.SystemAnnouncementRead{
				SysAnnID:  annID,
				AccountID: actor.ID,
				ReadAt:    now,
			}
			if err := b.SystemAnnouncementReads.Upsert(r.Context(), rec); err != nil {
				return err
			}
			out = rec
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionSystemAnnRead, annID, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		if bus != nil {
			bus.Publish(out.AccountID)
		}
		WriteJSON(w, http.StatusOK, SystemAnnouncementReadResponse{
			SysAnnID:  out.SysAnnID,
			AccountID: out.AccountID,
			ReadAt:    out.ReadAt,
		})
	}
}
