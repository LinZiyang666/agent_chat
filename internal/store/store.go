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

// RoomRepo persists Room rows (= the agentchat side of a Discord
// channel). DeleteByID cascades to memberships, messages, and
// message_states via the SQLite schema's FK ON DELETE CASCADE clauses.
type RoomRepo interface {
	Create(ctx context.Context, r *Room) error
	Get(ctx context.Context, id string) (*Room, error)
	GetByDiscordChannelID(ctx context.Context, discordChannelID string) (*Room, error)
	List(ctx context.Context, includeArchived bool) ([]*Room, error)
	ListByMember(ctx context.Context, accountID string, includeArchived bool) ([]*Room, error)
	Update(ctx context.Context, r *Room) error
	Delete(ctx context.Context, id string) error
}

// MembershipRepo persists Membership rows.
type MembershipRepo interface {
	Upsert(ctx context.Context, m *Membership) error
	Get(ctx context.Context, accountID, roomID string) (*Membership, error)
	ListByRoom(ctx context.Context, roomID string) ([]*Membership, error)
	ListByAccount(ctx context.Context, accountID string) ([]*Membership, error)
	// ListSubscribers returns members of roomID whose subscribed flag
	// is true. Used by the ingester to fan out new message_states.
	ListSubscribers(ctx context.Context, roomID string) ([]*Membership, error)
	SetSubscribed(ctx context.Context, accountID, roomID string, subscribed bool) error
	Delete(ctx context.Context, accountID, roomID string) error
}

// MessageRepo persists Message rows.
type MessageRepo interface {
	// Create inserts a new message. Returns Conflict if the
	// discord_msg_id already exists.
	Create(ctx context.Context, m *Message) error
	// CreateIgnoreConflict is the gateway-ingest variant: inserts if
	// the discord_msg_id is new, returns (nil, nil) and the existing
	// row id if it already exists. The first return value is the row
	// id that ended up in the database (either m.ID for a fresh
	// insert, or the prior id for an existing one).
	CreateIgnoreConflict(ctx context.Context, m *Message) (id string, inserted bool, err error)
	Get(ctx context.Context, id string) (*Message, error)
	GetByDiscordMsgID(ctx context.Context, discordMsgID string) (*Message, error)
	// List returns the most recent messages in a room, newest-first,
	// in (created_at DESC, id DESC) order so same-second writes have a
	// deterministic tie-break (UUIDv7 ids are themselves time-ordered;
	// see the M2-P3 audit trail). When BeforeID is set, the page
	// contains messages strictly older than that id and is again
	// ordered newest-first within the page — callers paging backwards
	// through history can reverse client-side if they want
	// chronological reading order.
	List(ctx context.Context, f MessageFilter) ([]*Message, error)
	// ApplySendMetadata writes back agentchat-local metadata onto an
	// existing message row. This is the M4-P3-010 fix: when the send
	// path loses the discord_msg_id INSERT race to the ingester (which
	// only knows Discord-native fields), the send-owned columns
	// (author_account_id, reply_to_msg_id, requires_ack, priority,
	// content_hash) get applied to the row created by the ingester.
	//
	// Only the send path should call this — never the ingester — so
	// external gateway events cannot overwrite send-owned metadata.
	// Returns NotFound if id does not exist.
	ApplySendMetadata(ctx context.Context, id string, m SendMetadata) error
}

// SendMetadata is the set of agentchat-local fields the send path
// owns on a message row. Used by MessageRepo.ApplySendMetadata.
type SendMetadata struct {
	AuthorAccountID string
	ReplyToMsgID    string
	RequiresAck     bool
	Priority        MessagePriority
	ContentHash     string
}

// MessageStateRepo persists MessageState rows (per-account read/reply
// flags).
type MessageStateRepo interface {
	Upsert(ctx context.Context, s *MessageState) error
	Get(ctx context.Context, messageID, accountID string) (*MessageState, error)
	ListByAccount(ctx context.Context, accountID string) ([]*MessageState, error)
}

// Bundle aggregates the repositories the daemon uses. Backends
// (e.g. internal/store/sqlite) construct a Bundle so callers receive a
// single value.
type Bundle struct {
	Accounts      AccountRepo
	Tokens        TokenRepo
	Audit         AuditRepo
	Rooms         RoomRepo
	Memberships   MembershipRepo
	Messages      MessageRepo
	MessageStates MessageStateRepo
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
