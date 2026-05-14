package v1

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LinZiyang666/agentchat/internal/account"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// CreateToken handles POST /v1/accounts/{id}/tokens.
//
// Transactional integrity (M2-P3-012 fix): the new token row AND the
// matching audit_log row are written inside one bundler.WithTx
// closure. The raw token is included in the response *exactly once*;
// the daemon never exposes it again.
//
// Performance (M2-P3-014 fix): the slow bcrypt step runs **outside**
// the transaction via PrepareToken so it does not hold the SQLite
// single-writer connection. Only the fast INSERT lives inside WithTx.
func CreateToken(bundler store.Bundler, mgr *auth.Manager, recorder *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		// Bcrypt and id/secret generation run before WithTx so the tx
		// stays short. The token row's CreatedAt is also fixed here;
		// it is stamped at preparation time, not commit time.
		raw, tk, err := mgr.PrepareToken(accountID)
		if err != nil {
			WriteError(w, err)
			return
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			// Confirm the target account exists in the same tx as the
			// token insert, so an inflight account.delete cannot race
			// past us and leave an orphan token. ON DELETE CASCADE
			// would otherwise still erase the token but we want a
			// clean NOT_FOUND error path here.
			if _, err := b.Accounts.Get(r.Context(), accountID); err != nil {
				return err
			}
			if err := mgr.PersistTokenVia(r.Context(), b.Tokens, tk); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionTokenCreate, tk.ID, map[string]any{"account_id": accountID})
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, CreateTokenResponse{
			Token: TokenToResponse(tk),
			Raw:   raw,
		})
	}
}

// ListTokens handles GET /v1/accounts/{id}/tokens.
func ListTokens(svc *account.Service, tokens store.TokenRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := chi.URLParam(r, "id")
		if _, err := svc.Get(r.Context(), accountID); err != nil {
			WriteError(w, err)
			return
		}
		ts, err := tokens.ListByAccount(r.Context(), accountID)
		if err != nil {
			WriteError(w, err)
			return
		}
		out := TokenListResponse{Tokens: make([]TokenResponse, 0, len(ts))}
		for _, t := range ts {
			out.Tokens = append(out.Tokens, TokenToResponse(t))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// RevokeToken handles DELETE /v1/tokens/{id}.
//
// Transactional: revoke + audit insert commit together (M2-P3-012 fix).
func RevokeToken(bundler store.Bundler, mgr *auth.Manager, recorder *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if err := mgr.RevokeVia(r.Context(), b.Tokens, id); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionTokenRevoke, id, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusNoContent, nil)
	}
}
