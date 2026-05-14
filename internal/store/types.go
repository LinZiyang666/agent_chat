package store

import "time"

// Role is an account's permission tier. The system has exactly two:
// admin and user (see docs/02-requirements-final.md §2).
type Role string

const (
	// RoleAdmin grants the bearer every operation the system supports.
	RoleAdmin Role = "admin"
	// RoleUser grants the bearer the limited surface in §2 of the
	// requirements document (read announcements, read own rooms' history,
	// send messages).
	RoleUser Role = "user"
)

// Valid reports whether r is one of the defined Role constants.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleUser
}

// LifecycleState is the chat-account lifecycle phase. See
// docs/02-requirements-final.md §3.1.
type LifecycleState string

const (
	LifecycleCreated  LifecycleState = "created"
	LifecycleOnline   LifecycleState = "online"
	LifecycleOffline  LifecycleState = "offline"
	LifecycleArchived LifecycleState = "archived"
	LifecycleDeleted  LifecycleState = "deleted"
)

// Valid reports whether s is one of the defined LifecycleState constants.
func (s LifecycleState) Valid() bool {
	switch s {
	case LifecycleCreated, LifecycleOnline, LifecycleOffline,
		LifecycleArchived, LifecycleDeleted:
		return true
	}
	return false
}

// Account is a stored account row. Times are wall-clock UTC.
type Account struct {
	ID             string
	Name           string
	Role           Role
	LifecycleState LifecycleState
	// BotTokenEnc is the AES-GCM ciphertext of the Discord bot token.
	// Empty until M3 plugs in real Discord bots.
	BotTokenEnc []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Token is an API-token row. The plaintext secret half is never stored;
// only its bcrypt hash.
type Token struct {
	ID         string
	AccountID  string
	Hash       []byte
	CreatedAt  time.Time
	RevokedAt  *time.Time // non-nil means revoked
	LastUsedAt *time.Time
}

// Revoked reports whether RevokedAt is set.
func (t *Token) Revoked() bool { return t != nil && t.RevokedAt != nil }

// AuditEntry is one administrative-action record.
type AuditEntry struct {
	ID        string
	AccountID string // who performed the action
	Action    string // dotted verb, e.g. "account.create"
	Target    string // target resource ID (may be empty)
	Payload   string // JSON-encoded extra context (may be empty)
	CreatedAt time.Time
}

// AuditFilter narrows AuditRepo.List results.
type AuditFilter struct {
	AccountID string     // empty = all accounts
	Since     *time.Time // nil = no lower bound
	Limit     int        // 0 = repo's default cap
}
