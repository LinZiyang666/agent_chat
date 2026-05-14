// Package middleware contains daemon-side HTTP middleware (panic
// recovery, request logging). The authentication middleware lives in
// internal/auth because it needs the auth Manager type.
package middleware

import (
	"log/slog"
	"net/http"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Recover catches panics in downstream handlers, logs them, and returns
// a structured INTERNAL error response. Without this, a panic would
// kill the daemon process.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					log.Error("handler panic",
						"path", r.URL.Path,
						"method", r.Method,
						"panic", rv,
					)
					apiv1.WriteError(w, errcode.New(errcode.Internal, "internal server error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
