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

// CreateAccount handles POST /v1/accounts.
//
// Transactional integrity (fix for M2-P3-012): the new account row
// AND the matching audit_log row are written inside one bundler.WithTx
// closure. Either both commit or both roll back — there is no path
// where an account is created without its audit entry.
func CreateAccount(svc *account.Service, bundler store.Bundler, recorder *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateAccountRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		// Validate + construct without touching the DB.
		acc, err := svc.Build(req.Name, store.Role(req.Role))
		if err != nil {
			WriteError(w, err)
			return
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			if err := b.Accounts.Create(r.Context(), acc); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAccountCreate, acc.ID, map[string]any{
					"name": acc.Name,
					"role": string(acc.Role),
				})
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, AccountToResponse(acc))
	}
}

// ListAccounts handles GET /v1/accounts.
func ListAccounts(svc *account.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accs, err := svc.List(r.Context())
		if err != nil {
			WriteError(w, err)
			return
		}
		out := AccountListResponse{Accounts: make([]AccountResponse, 0, len(accs))}
		for _, a := range accs {
			out.Accounts = append(out.Accounts, AccountToResponse(a))
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

// GetAccount handles GET /v1/accounts/{id}.
func GetAccount(svc *account.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		a, err := svc.Get(r.Context(), id)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, AccountToResponse(a))
	}
}

// UpdateAccount handles PATCH /v1/accounts/{id}.
//
// Atomicity (M2-P3-002): every requested field is validated *before*
// the DB write inside the transaction.
// Auditing (M2-P3-003): one account.update entry is recorded per
// non-empty change set, in the same transaction as the mutation
// (M2-P3-012 fix). No-op requests skip both writes.
func UpdateAccount(svc *account.Service, bundler store.Bundler, recorder *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req UpdateAccountRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		var roleP *store.Role
		if req.Role != nil {
			rr := store.Role(*req.Role)
			roleP = &rr
		}
		updateReq := account.UpdateRequest{Name: req.Name, Role: roleP}

		var updated *store.Account
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			current, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			planned, changeList, err := svc.PlanUpdate(current, updateReq)
			if err != nil {
				return err
			}
			if len(changeList) == 0 {
				updated = current
				return nil
			}
			if err := b.Accounts.Update(r.Context(), planned); err != nil {
				return err
			}
			updated = planned
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAccountUpdate, id, map[string]any{"changes": changeList})
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, AccountToResponse(updated))
	}
}

// DeleteAccount handles DELETE /v1/accounts/{id}.
//
// Transactional: the account row and audit row are removed/written
// inside one transaction (M2-P3-012 fix). The 404 path produces no
// audit row.
func DeleteAccount(bundler store.Bundler, recorder *audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			if err := b.Accounts.Delete(r.Context(), id); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAccountDelete, id, map[string]any{"name": a.Name})
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusNoContent, nil)
	}
}
