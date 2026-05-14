package store

import (
	"context"
	"time"
)

// AccountRepo persists accounts. All methods take a context for
// cancellation; the SQLite implementation honors it on every query.
type AccountRepo interface {
	Create(ctx context.Context, a *Account) error
	Get(ctx context.Context, id string) (*Account, error)
	GetByName(ctx context.Context, name string) (*Account, error)
	List(ctx context.Context) ([]*Account, error)
	Update(ctx context.Context, a *Account) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
}

// TokenRepo persists API tokens. Tokens are looked up by their ID
// (which is exposed in the raw token string); the secret half is
// verified by bcrypt in the auth package.
type TokenRepo interface {
	Create(ctx context.Context, t *Token) error
	Get(ctx context.Context, id string) (*Token, error)
	ListByAccount(ctx context.Context, accountID string) ([]*Token, error)
	Revoke(ctx context.Context, id string, at time.Time) error
	TouchLastUsed(ctx context.Context, id string, at time.Time) error
}

// AuditRepo persists administrative-action records.
type AuditRepo interface {
	Record(ctx context.Context, e *AuditEntry) error
	List(ctx context.Context, f AuditFilter) ([]*AuditEntry, error)
}

// Bundle aggregates the three repositories the daemon uses. Backends
// (e.g. internal/store/sqlite) construct a Bundle so callers receive a
// single value.
type Bundle struct {
	Accounts AccountRepo
	Tokens   TokenRepo
	Audit    AuditRepo
}

// Bundler runs a closure inside a single backend-level transaction.
// The Bundle passed to fn writes through that transaction, so any
// combination of mutations + audit insert lands atomically (or rolls
// back together on error).
//
// This is the seam that fixes M2-P3-012: handlers that need to record
// audit alongside a mutation use WithTx to guarantee the two writes
// commit or fail together, eliminating the "mutation persists, audit
// did not" inconsistency.
type Bundler interface {
	WithTx(ctx context.Context, fn func(b Bundle) error) error
}
