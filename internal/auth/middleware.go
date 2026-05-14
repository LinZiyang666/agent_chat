package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// ctxKey is the unexported type used for context-value keys to prevent
// collisions with keys from other packages.
type ctxKey int

const (
	ctxKeyAccount ctxKey = iota + 1
	ctxKeyTokenID
)

// AccountFromContext returns the authenticated account, or false if
// the request was not authenticated.
func AccountFromContext(ctx context.Context) (*store.Account, bool) {
	a, ok := ctx.Value(ctxKeyAccount).(*store.Account)
	return a, ok
}

// TokenIDFromContext returns the verified token's ID.
func TokenIDFromContext(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(ctxKeyTokenID).(string)
	return s, ok
}

// withAccount returns a child context carrying the authenticated
// account; exported for tests that fabricate authenticated requests
// without going through the middleware.
func WithAccount(parent context.Context, a *store.Account, tokenID string) context.Context {
	ctx := context.WithValue(parent, ctxKeyAccount, a)
	return context.WithValue(ctx, ctxKeyTokenID, tokenID)
}

// ErrorWriter writes structured error responses (status + JSON body) to
// the HTTP response. Defined as a function type so the middleware does
// not need to import internal/api.
type ErrorWriter func(http.ResponseWriter, error)

// Middleware enforces Bearer-token authentication on every request and
// attaches the resolved account to the request context.
type Middleware struct {
	manager  *Manager
	accounts store.AccountRepo
	writeErr ErrorWriter
}

// NewMiddleware wires the dependencies; writeErr is called for every
// unauthorized or internal-error path so callers can centralize error
// rendering.
func NewMiddleware(m *Manager, accounts store.AccountRepo, writeErr ErrorWriter) *Middleware {
	return &Middleware{manager: m, accounts: accounts, writeErr: writeErr}
}

// Handler returns an http.Handler that authenticates the request,
// then delegates to next.
func (mw *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			mw.writeErr(w, err)
			return
		}
		accountID, tokenID, err := mw.manager.Verify(r.Context(), raw)
		if err != nil {
			mw.writeErr(w, err)
			return
		}
		a, err := mw.accounts.Get(r.Context(), accountID)
		if err != nil {
			// The token is valid but the account vanished — treat as
			// AuthInvalid so the client retries cleanly.
			if e, ok := errcode.As(err); ok && e.Code == errcode.NotFound {
				mw.writeErr(w, errcode.New(errcode.AuthInvalid, "account no longer exists"))
				return
			}
			mw.writeErr(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithAccount(r.Context(), a, tokenID)))
	})
}

// RequireAdmin wraps next so only callers with RoleAdmin reach it. The
// auth middleware must run first so the account is in context.
func RequireAdmin(writeErr ErrorWriter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := AccountFromContext(r.Context())
		if !ok {
			writeErr(w, errcode.New(errcode.AuthMissing, "no account in context"))
			return
		}
		if a.Role != store.RoleAdmin {
			writeErr(w, errcode.New(errcode.PermDenied, "admin role required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

const bearerPrefix = "Bearer "

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errcode.New(errcode.AuthMissing, "missing Authorization header")
	}
	if !strings.HasPrefix(h, bearerPrefix) {
		return "", errcode.New(errcode.AuthInvalid, "Authorization scheme is not Bearer")
	}
	raw := strings.TrimSpace(h[len(bearerPrefix):])
	if raw == "" {
		return "", errcode.New(errcode.AuthInvalid, "empty bearer token")
	}
	return raw, nil
}
