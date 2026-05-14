// Package audit records administrative actions (account CRUD, role
// changes, token revocations) to a persistent log for accountability.
// The actual storage is delegated to a store.AuditRepo; this package
// adds policy: ID generation, JSON payload encoding, time injection,
// and a stable list of action verbs.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// Action is the canonical verb for an audited operation. Use these
// constants in code, not raw strings, so a typo becomes a compile error.
type Action string

const (
	ActionAccountCreate     Action = "account.create"
	ActionAccountUpdate     Action = "account.update"
	ActionAccountDelete     Action = "account.delete"
	ActionAccountSetDiscord Action = "account.discord_set"
	ActionAccountOnline     Action = "account.online"
	ActionAccountOffline    Action = "account.offline"
	ActionTokenCreate       Action = "token.create"
	ActionTokenRevoke       Action = "token.revoke"
)

// Deprecated aliases retained so older payloads (or external readers
// inspecting the audit_log table) still resolve to a Go constant. New
// code should use ActionAccountUpdate, which records the full set of
// changed fields in the payload.
const ActionAccountSetRole Action = "account.set_role"

// Recorder writes audit entries to the underlying repo.
type Recorder struct {
	repo store.AuditRepo
	now  func() time.Time
}

// NewRecorder constructs a Recorder backed by repo.
func NewRecorder(repo store.AuditRepo) *Recorder {
	return &Recorder{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source. Tests only.
func (r *Recorder) SetClock(now func() time.Time) { r.now = now }

// Record writes one audit entry. accountID is the actor (the
// authenticated account performing the action). target is the resource
// being acted on; payload is arbitrary JSON-encodable context (may be
// nil). The entry's ID and CreatedAt are filled in automatically.
func (r *Recorder) Record(ctx context.Context, accountID string, action Action, target string, payload any) error {
	return r.RecordVia(ctx, r.repo, accountID, action, target, payload)
}

// RecordVia is identical to Record but writes through the given
// AuditRepo instead of the Recorder's default. Used by handlers that
// share a transaction with a sibling mutation — they pass the
// transaction's audit repo so both writes commit (or roll back) as
// one unit. Fix path for M2-P3-012.
func (r *Recorder) RecordVia(ctx context.Context, repo store.AuditRepo, accountID string, action Action, target string, payload any) error {
	if action == "" {
		return errcode.New(errcode.InvalidArgument, "action is empty")
	}
	if repo == nil {
		return errcode.New(errcode.Internal, "audit repo is nil")
	}
	var payloadJSON string
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return errcode.Wrap(err, errcode.Internal, "marshal audit payload")
		}
		payloadJSON = string(b)
	}
	u, err := uuid.NewV7()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "uuidv7")
	}
	return repo.Record(ctx, &store.AuditEntry{
		ID:        u.String(),
		AccountID: accountID,
		Action:    string(action),
		Target:    target,
		Payload:   payloadJSON,
		CreatedAt: r.now(),
	})
}

// List forwards to the repo. Provided here so callers depend on the
// audit package, not the store interface directly.
func (r *Recorder) List(ctx context.Context, f store.AuditFilter) ([]*store.AuditEntry, error) {
	return r.repo.List(ctx, f)
}
