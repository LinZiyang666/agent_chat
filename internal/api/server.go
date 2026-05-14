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
					apiv1.SetDiscord(d.Accounts, d.Bundler, d.Audit, d.MasterKey))
				r.Post("/accounts/{id}/online",
					apiv1.OnlineAccount(d.Accounts, d.Connector, d.Bundler, d.Audit, d.MasterKey))
				r.Post("/accounts/{id}/offline",
					apiv1.OfflineAccount(d.Accounts, d.Connector, d.Bundler, d.Audit))
				r.Get("/accounts/{id}/status",
					apiv1.AccountStatus(d.Accounts, d.Connector))

				// M3 debug surface — pre-M4 rooms subsystem.
				r.Post("/debug/send", apiv1.DebugSend(d.Connector))
				r.Get("/debug/events", apiv1.DebugEvents(d.Connector))
			})
		})
	})

	return r
}
