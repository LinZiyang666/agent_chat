package v1

import "net/http"

// HealthzResponse is the body of GET /v1/healthz.
type HealthzResponse struct {
	Status string `json:"status"`
}

// Healthz is a public, unauthenticated liveness probe. M8 may extend
// this to include subsystem health; for M2 it just confirms the
// daemon is up.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, HealthzResponse{Status: "ok"})
}
