package v1

import (
	"net/http"

	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Whoami returns the authenticated account and the token id that
// authorized this request. The auth middleware must run first.
func Whoami(w http.ResponseWriter, r *http.Request) {
	a, ok := auth.AccountFromContext(r.Context())
	if !ok {
		WriteError(w, errcode.New(errcode.AuthMissing, "no account in context"))
		return
	}
	tokenID, _ := auth.TokenIDFromContext(r.Context())
	WriteJSON(w, http.StatusOK, WhoamiResponse{
		Account: AccountToResponse(a),
		TokenID: tokenID,
	})
}
