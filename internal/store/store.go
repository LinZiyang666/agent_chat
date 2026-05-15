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
	// ListByAccountWithRooms returns one row per membership joined
	// with the corresponding room. Used by the M5 aggregator to
	// avoid N+1 Rooms.Get calls (M5-P3-006 fix). The result is
	// ordered by joined_at ASC (matching ListByAccount).
	ListByAccountWithRooms(ctx context.Context, accountID string) ([]*MembershipWithRoom, error)
	// ListSubscribers returns members of roomID whose subscribed flag
	// is true. Used by the ingester to fan out new message_states.
	ListSubscribers(ctx context.Context, roomID string) ([]*Membership, error)
	SetSubscribed(ctx context.Context, accountID, roomID string, subscribed bool) error
	Delete(ctx context.Context, accountID, roomID string) error
}

// MembershipWithRoom is the joined view used by
// MembershipRepo.ListByAccountWithRooms.
type MembershipWithRoom struct {
	Membership *Membership
	Room       *Room
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
	// LatestPerRoomForMember returns the most recent message in each
	// non-archived room the account is a member of (subscribed or
	// not). Used by the M5 aggregator's "recently active" dimension.
	// Rooms with no messages at all are omitted from the map.
	LatestPerRoomForMember(ctx context.Context, accountID string) (map[string]*Message, error)
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
	MentionAll      bool
}

// MessageStateRepo persists MessageState rows (per-account read/reply
// flags).
type MessageStateRepo interface {
	Upsert(ctx context.Context, s *MessageState) error
	Get(ctx context.Context, messageID, accountID string) (*MessageState, error)
	ListByAccount(ctx context.Context, accountID string) ([]*MessageState, error)

	// M5 state-aggregator read paths. All are scoped to the
	// caller's *subscribed and non-archived* rooms — per
	// requirements §5.2.1 the primary state UI aggregates
	// "属 + 已订阅" rooms only, and archived rooms are not active
	// chat surfaces (M5-P3-001 fix).
	//
	// Totals queries (Count*) and feed queries (List*) are split
	// (M5-P3-002 fix): the feed APIs cap at `limit` for display,
	// but Totals must reflect the full unbounded count.

	// CountUnreadForSubscribed returns the total number of
	// message_state rows whose read_at IS NULL and whose room is
	// subscribed and not archived.
	CountUnreadForSubscribed(ctx context.Context, accountID string) (int, error)
	// CountMentionsForSubscribed counts unread messages in
	// subscribed non-archived rooms whose content contains the
	// literal "<@botUserID>" mention token. Used for
	// Totals.Mentions (the visible feed is capped via
	// ListMentionsForSubscribed).
	CountMentionsForSubscribed(ctx context.Context, accountID, botUserID string) (int, error)
	// CountPendingAcksForSubscribed counts requires_ack messages
	// in subscribed non-archived rooms whose replied_at IS NULL.
	CountPendingAcksForSubscribed(ctx context.Context, accountID string) (int, error)
	// CountPriorityForSubscribed counts unread urgent+system
	// messages in subscribed non-archived rooms.
	CountPriorityForSubscribed(ctx context.Context, accountID string) (int, error)
	// UnreadCountByRoomForSubscribed returns a roomID -> unread
	// count map for the account's subscribed non-archived rooms.
	UnreadCountByRoomForSubscribed(ctx context.Context, accountID string) (map[string]int, error)
	// ListMentionsForSubscribed returns unread messages in subscribed
	// non-archived rooms whose content contains the literal
	// "<@botUserID>" mention token. Newest-first, capped at limit
	// (default 50). Counters for Totals use CountMentionsForSubscribed.
	ListMentionsForSubscribed(ctx context.Context, accountID, botUserID string, limit int) ([]*Message, error)
	// ListPendingAcksForSubscribed returns messages in subscribed
	// non-archived rooms with requires_ack=1 whose state.replied_at
	// IS NULL. Newest-first, capped at limit.
	ListPendingAcksForSubscribed(ctx context.Context, accountID string, limit int) ([]*Message, error)
	// ListPriorityForSubscribed returns unread messages in subscribed
	// non-archived rooms whose priority is 'urgent' or 'system'.
	// Newest-first, capped at limit.
	ListPriorityForSubscribed(ctx context.Context, accountID string, limit int) ([]*Message, error)
}

// AnnouncementRepo persists room-scoped announcements (M6). Versions
// are monotonically increasing within a room: every Create assigns
// prior_max_version+1. The GET endpoint exposes only the latest row.
type AnnouncementRepo interface {
	// Create inserts a new announcement with version =
	// NextVersion(roomID). Returns the assigned row.
	Create(ctx context.Context, a *Announcement) error
	// Get returns the announcement by id.
	Get(ctx context.Context, id string) (*Announcement, error)
	// Latest returns the highest-version row in roomID, or
	// errcode.NotFound if the room has none yet.
	Latest(ctx context.Context, roomID string) (*Announcement, error)
	// NextVersion returns 1 if the room has no announcements yet,
	// else max(version)+1. Used inside Create's transaction.
	NextVersion(ctx context.Context, roomID string) (int, error)
}

// AnnouncementReadRepo records per-account acks of room announcements
// (M6). Absence of a row = unread.
type AnnouncementReadRepo interface {
	// Upsert marks (announcementID, accountID) as read at the given
	// instant. Re-acks are idempotent (read_at is overwritten with
	// the new time).
	Upsert(ctx context.Context, r *AnnouncementRead) error
	// IsRead reports whether the account has ack'd this announcement
	// id at any point.
	IsRead(ctx context.Context, announcementID, accountID string) (bool, error)
	// CountUnreadForAccount returns how many announcements exist in
	// rooms the account is a *member* of (subscribed or not, archived
	// or not — announcements are mandatory-read across the membership)
	// where no ack row exists. Used by the M5 aggregator extension.
	CountUnreadForAccount(ctx context.Context, accountID string) (int, error)
	// ListUnreadForAccount returns up to limit unread announcements
	// for the account, newest-first, joined with the announcement row.
	// Each Announcement also carries its room id so the aggregator can
	// surface the room name.
	ListUnreadForAccount(ctx context.Context, accountID string, limit int) ([]*Announcement, error)
}

// SystemAnnouncementRepo persists global admin announcements (M6).
type SystemAnnouncementRepo interface {
	Create(ctx context.Context, a *SystemAnnouncement) error
	Get(ctx context.Context, id string) (*SystemAnnouncement, error)
	// List returns all system announcements, newest-first, capped at
	// limit (0 = repo default).
	List(ctx context.Context, limit int) ([]*SystemAnnouncement, error)
}

// SystemAnnouncementReadRepo records per-account acks of system
// announcements (M6). Same absence-=-unread semantics.
type SystemAnnouncementReadRepo interface {
	Upsert(ctx context.Context, r *SystemAnnouncementRead) error
	IsRead(ctx context.Context, sysAnnID, accountID string) (bool, error)
	// CountUnreadForAccount returns how many system announcements
	// exist that the account has not ack'd.
	CountUnreadForAccount(ctx context.Context, accountID string) (int, error)
	// ListUnreadForAccount returns up to limit unread system
	// announcements, newest-first.
	ListUnreadForAccount(ctx context.Context, accountID string, limit int) ([]*SystemAnnouncement, error)
}

// Bundle aggregates the repositories the daemon uses. Backends
// (e.g. internal/store/sqlite) construct a Bundle so callers receive a
// single value.
type Bundle struct {
	Accounts                AccountRepo
	Tokens                  TokenRepo
	Audit                   AuditRepo
	Rooms                   RoomRepo
	Memberships             MembershipRepo
	Messages                MessageRepo
	MessageStates           MessageStateRepo
	Announcements           AnnouncementRepo
	AnnouncementReads       AnnouncementReadRepo
	SystemAnnouncements     SystemAnnouncementRepo
	SystemAnnouncementReads SystemAnnouncementReadRepo
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
