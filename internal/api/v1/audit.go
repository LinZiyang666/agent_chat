package v1

import (
	"net/http"
	"strconv"

	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// ListAudit handles GET /v1/audit.
//
// Supported query parameters:
//
//	?account=<id>  filter by acting account
//	?since=<rfc3339> filter to entries at-or-after this instant
//	?limit=<int>   cap result count (default: store-side cap)
func ListAudit(rec *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		since, err := ParseSinceParam(q.Get("since"))
		if err != nil {
			WriteError(w, err)
			return
		}
		var limit int
		if s := q.Get("limit"); s != "" {
			n, perr := strconv.Atoi(s)
			if perr != nil || n < 0 {
				WriteError(w, errcode.New(errcode.InvalidArgument, "limit must be a non-negative integer"))
				return
			}
			limit = n
		}
		f := store.AuditFilter{
			AccountID: q.Get("account"),
			Since:     since,
			Limit:     limit,
		}
		entries, err := rec.List(r.Context(), f)
		if err != nil {
			WriteError(w, err)
			return
		}
		out := AuditListResponse{Entries: make([]AuditEntryResponse, 0, len(entries))}
		for _, e := range entries {
			out.Entries = append(out.Entries, AuditToResponse(e))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}
