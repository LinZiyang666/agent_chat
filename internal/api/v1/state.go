package v1

import (
	"encoding/json"
	"net/http"

	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/state"
)

// GetState handles GET /v1/state.
//
// Builds a one-shot snapshot of the calling account's state view
// (the 8 dimensions from requirements §5.2). The bus's version
// counter increments by one for every snapshot it builds, including
// the one-shot ones served here.
func GetState(bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bus == nil {
			WriteError(w, errcode.New(errcode.Internal, "state bus is not configured"))
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		snap, err := bus.BuildNow(r.Context(), actor.ID)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, snap)
	}
}

// WatchState handles GET /v1/state/watch. Streams NDJSON: the first
// frame is the current snapshot, then one frame per debounced state
// change for as long as the connection stays open. Idle connections
// emit no bytes (per the §5.2 D5 contract).
//
// Implementation notes:
//   - We don't read from r.Body; the connection-state hook is
//     r.Context().Done() (chi/net/http closes the request context
//     when the client hangs up).
//   - We hold no per-account locks while writing; the Bus's
//     non-blocking send means a slow client drops snapshots rather
//     than back-pressuring the daemon.
//   - `?since=<version>` is reserved by the roadmap but explicitly
//     not implemented in M5 (M5-P3-005 audit decision). We reject
//     the query parameter with InvalidArgument rather than silently
//     ignoring it, so callers don't write code that depends on
//     replay semantics that don't exist yet.
func WatchState(bus *state.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bus == nil {
			WriteError(w, errcode.New(errcode.Internal, "state bus is not configured"))
			return
		}
		if r.URL.Query().Has("since") {
			WriteError(w, errcode.New(errcode.InvalidArgument,
				"since=<version> resume semantics are not implemented in M5; deferred to a later milestone (audit decision M5-P3-005). Drop the parameter to subscribe from the current snapshot."))
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "response writer does not support streaming"))
			return
		}
		sub, err := bus.Subscribe(r.Context(), actor.ID)
		if err != nil {
			WriteError(w, err)
			return
		}
		defer bus.Unsubscribe(sub)

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		enc := json.NewEncoder(w)
		for {
			select {
			case <-r.Context().Done():
				return
			case snap, open := <-sub.C:
				if !open {
					return
				}
				if err := enc.Encode(snap); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
