package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LinZiyang666/agentchat/internal/account"
	apimw "github.com/LinZiyang666/agentchat/internal/api/middleware"
	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/message"
	"github.com/LinZiyang666/agentchat/internal/state"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// Deps bundles every collaborator the API router needs. Construction
// is the daemon's job (in agentchatd serve); the router itself is
// pure-glue.
type Deps struct {
	Log         *slog.Logger
	Accounts    *account.Service
	AccountRepo store.AccountRepo
	TokenRepo   store.TokenRepo
	Auth        *auth.Manager
	Audit       *audit.Recorder
	// Bundler runs mutation + audit pairs inside a single backend
	// transaction. Required by every mutating /v1/accounts and
	// /v1/tokens handler (M2-P3-012 fix).
	Bundler store.Bundler
	// Connector owns live Provider instances. M3+ endpoints
	// (set-discord, online, offline, status, debug send/events) need it.
	Connector *connector.Connector
	// MasterKey is the AES-GCM key used to encrypt/decrypt bot tokens
	// at rest. Loaded from disk by the daemon.
	MasterKey []byte
	// Ingester is the M4 inbound-message ingester. OnlineAccount
	// attaches an account to it after a successful Connect; the
	// background goroutine writes message rows + per-subscriber state
	// inside bundler.WithTx.
	Ingester *message.Ingester
	// StateBus is the M5 state-fan-out engine. Mutation handlers call
	// Publish(accountID) after their tx commits to schedule a
	// debounced snapshot rebuild for the watchers on that account.
	// Handlers nil-check this field so test rigs that don't need
	// state can leave it empty.
	StateBus *state.Bus
}

// NewRouter builds the chi router with the full /v1 surface. Returned
// value implements http.Handler.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(apimw.Recover(d.Log))
	r.Use(apimw.Logger(d.Log))

	// Public.
	r.Get("/v1/healthz", apiv1.Healthz)

	// Authenticated routes share the auth middleware.
	authMW := auth.NewMiddleware(d.Auth, d.AccountRepo, apiv1.WriteError)
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authMW.Handler)

			r.Get("/whoami", apiv1.Whoami)

			// Admin-only sub-tree.
			r.Group(func(r chi.Router) {
				r.Use(func(next http.Handler) http.Handler {
					return auth.RequireAdmin(apiv1.WriteError, next)
				})
				r.Post("/accounts", apiv1.CreateAccount(d.Accounts, d.Bundler, d.Audit))
				r.Get("/accounts", apiv1.ListAccounts(d.Accounts))
				r.Get("/accounts/{id}", apiv1.GetAccount(d.Accounts))
				r.Patch("/accounts/{id}", apiv1.UpdateAccount(d.Accounts, d.Bundler, d.Audit))
				r.Delete("/accounts/{id}", apiv1.DeleteAccount(d.Bundler, d.Audit, d.Connector))

				r.Post("/accounts/{id}/tokens", apiv1.CreateToken(d.Bundler, d.Auth, d.Audit))
				r.Get("/accounts/{id}/tokens", apiv1.ListTokens(d.Accounts, d.TokenRepo))
				r.Delete("/tokens/{id}", apiv1.RevokeToken(d.Bundler, d.Auth, d.Audit))

				r.Get("/audit", apiv1.ListAudit(d.Audit))

				// M3: Discord-side wiring (admin-only).
				r.Post("/accounts/{id}/discord",
					apiv1.SetDiscord(d.Bundler, d.Audit, d.MasterKey))
				r.Post("/accounts/{id}/online",
					apiv1.OnlineAccount(d.Accounts, d.Connector, d.Bundler, d.Audit, d.MasterKey, d.Ingester))
				r.Post("/accounts/{id}/offline",
					apiv1.OfflineAccount(d.Accounts, d.Connector, d.Bundler, d.Audit, d.Ingester))
				r.Get("/accounts/{id}/status",
					apiv1.AccountStatus(d.Accounts, d.Connector))

				// M3 debug surface — pre-M4 rooms subsystem.
				r.Post("/debug/send", apiv1.DebugSend(d.Connector, d.Audit))
				r.Get("/debug/events", apiv1.DebugEvents(d.Connector))

				// M4: rooms (admin-only mutations except member self-PATCH).
				r.Post("/rooms", apiv1.CreateRoom(d.Connector, d.Bundler, d.Audit, d.StateBus))
				r.Patch("/rooms/{id}", apiv1.UpdateRoom(d.Connector, d.Bundler, d.Audit, d.StateBus))
				r.Post("/rooms/{id}/archive", apiv1.ArchiveRoom(d.Bundler, d.Audit, d.StateBus))
				r.Delete("/rooms/{id}", apiv1.DeleteRoom(d.Connector, d.Bundler, d.Audit, d.StateBus))
				r.Post("/rooms/{id}/members", apiv1.InviteMember(d.Connector, d.Bundler, d.Audit, d.StateBus))
				r.Delete("/rooms/{id}/members/{account_id}", apiv1.KickMember(d.Connector, d.Bundler, d.Audit, d.StateBus))

				// M6: system announcements (admin-only create).
				r.Post("/system/announcements", apiv1.CreateSystemAnnouncement(d.Bundler, d.Audit, d.StateBus))
			})

			// Member-and-admin operations (auth required, no admin gate).
			r.Get("/rooms", apiv1.ListRooms(d.Bundler))
			r.Get("/rooms/{id}", apiv1.GetRoom(d.Bundler))
			r.Get("/rooms/{id}/members", apiv1.ListMembers(d.Bundler))
			r.Patch("/memberships/{room_id}", apiv1.UpdateMembership(d.Bundler, d.Audit, d.StateBus))

			r.Post("/rooms/{id}/messages", apiv1.SendMessage(d.Connector, d.Bundler, d.Audit, d.StateBus))
			r.Get("/rooms/{id}/messages", apiv1.ListMessages(d.Bundler))
			r.Post("/messages/{id}/read", apiv1.MarkRead(d.Bundler, d.Audit, d.StateBus))
			r.Post("/messages/{id}/reply-ack", apiv1.ReplyAck(d.Bundler, d.Audit, d.StateBus))

			// M6: announcements (room-scoped + per-announcement ack).
			// Any member of the room can post (admins bypass the member
			// gate); the GET endpoint always returns the latest version
			// with the caller's per-version read flag.
			//
			// CreateAnnouncement also best-effort-mirrors the content
			// to the Discord channel via the actor's Provider so
			// non-agentchat-aware humans in #channel see the post.
			r.Post("/rooms/{id}/announcement", apiv1.CreateAnnouncement(d.Connector, d.Bundler, d.Audit, d.StateBus, d.Log))
			r.Get("/rooms/{id}/announcement", apiv1.GetAnnouncement(d.Bundler))
			r.Post("/announcements/{id}/read", apiv1.MarkAnnouncementRead(d.Bundler, d.Audit, d.StateBus))

			// M6: system announcements (admin-only create above; list +
			// per-row ack open to any authenticated caller).
			r.Get("/system/announcements", apiv1.ListSystemAnnouncements(d.Bundler))
			r.Post("/system/announcements/{id}/read", apiv1.MarkSystemAnnouncementRead(d.Bundler, d.Audit, d.StateBus))

			// M5: state view (one-shot + watch NDJSON).
			r.Get("/state", apiv1.GetState(d.StateBus))
			r.Get("/state/watch", apiv1.WatchState(d.StateBus))
		})
	})

	return r
}
