package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder wraps an http.ResponseWriter to remember the status
// code while preserving the http.Flusher capability that streaming
// handlers depend on (e.g. /v1/debug/events NDJSON).
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush delegates to the underlying ResponseWriter when it supports
// http.Flusher. Without this explicit forward, embedded-interface
// method promotion does NOT carry Flush over, since Flush is not in
// http.ResponseWriter's method set — breaking any streaming handler.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logger emits one slog.Info record per request with method, path,
// status, duration, and byte count.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
