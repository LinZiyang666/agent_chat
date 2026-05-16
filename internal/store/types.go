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
	// BotUserID is the Discord-side user id of the bot, captured the
	// first time the account goes online (M4). Empty until then. The
	// inbound-message ingester uses it to map Discord author_ids back
	// to agentchat account ids.
	BotUserID string
	CreatedAt time.Time
	UpdatedAt time.Time
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

// MessagePriority is the priority band of a Message. Maps 1:1 to the
// priority CHECK constraint on the messages table.
type MessagePriority string

const (
	// PriorityNormal is the default for user-sent messages.
	PriorityNormal MessagePriority = "normal"
	// PriorityUrgent flags a message for the urgent / @-me section of
	// the state UI (M5).
	PriorityUrgent MessagePriority = "urgent"
	// PrioritySystem is reserved for system announcements (M6).
	PrioritySystem MessagePriority = "system"
)

// Valid reports whether p is one of the defined priorities.
func (p MessagePriority) Valid() bool {
	switch p {
	case PriorityNormal, PriorityUrgent, PrioritySystem:
		return true
	}
	return false
}

// Room is one persisted room row. A room is a 1:1 mapping onto a
// Discord channel inside the configured guild (see requirements §3.2
// and roadmap §5).
type Room struct {
	ID               string
	DiscordChannelID string
	Name             string
	Archived         bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Membership is the account ↔ room edge. Subscribed differentiates
// "属 + 旁观" (subscribed=false) from "属 + 订阅" (subscribed=true),
// per requirements §4.
type Membership struct {
	AccountID  string
	RoomID     string
	Subscribed bool
	JoinedAt   time.Time
}

// Message is one persisted message row. AuthorAccountID is empty when
// the message came from a Discord user that is not one of our
// agentchat bots (it is still ingested for history).
//
// DiscordMsgID is the Discord-assigned snowflake; it is UNIQUE in the
// schema so the send-path and the gateway-ingest path can converge on
// the same row.
type Message struct {
	ID              string
	RoomID          string
	AuthorAccountID string
	DiscordMsgID    string
	Content         string
	ReplyToMsgID    string
	RequiresAck     bool
	Priority        MessagePriority
	CreatedAt       time.Time
	ContentHash     string
	// MentionAll is the @all flag (M6). When true, every member of the
	// message's room counts the message in their mentions feed, in
	// addition to literal "<@bot_user_id>" matches.
	//
	// Deprecated (M9 Phase 1): superseded by MentionEveryone +
	// message_mentions table. Still written by the M6 send path
	// (--all) for backward compatibility through the cutover; will
	// be removed in M9 Phase 2.
	MentionAll bool
	// MentionEveryone mirrors Discord's MESSAGE_CREATE.mention_everyone
	// (M9). Replaces MentionAll going forward. State queries read this
	// column; ingestion path sets it from the bot event, send path
	// sets it true when the legacy --all flag is on.
	MentionEveryone bool
}

// MessageState is one per-account read/reply state row. Both
// timestamps are nil until the account performs the corresponding
// action.
type MessageState struct {
	MessageID string
	AccountID string
	ReadAt    *time.Time
	RepliedAt *time.Time
}

// MessageFilter narrows MessageRepo.List results.
type MessageFilter struct {
	RoomID   string // required for List
	BeforeID string // empty = no upper bound; otherwise: messages strictly older than this id
	Limit    int    // 0 = repo's default cap (50)
}

// Reaction is reserved for M5+. M4 creates the table but does not
// surface a repo API yet; the type lives here so future migrations
// don't need to reshuffle.
type Reaction struct {
	MessageID string
	AccountID string
	Emoji     string
	CreatedAt time.Time
}

// Announcement is one room-scoped announcement row (M6). Each call to
// "room announce" produces a fresh row with version = prior_version+1,
// preserving history. The GET endpoint returns only the highest-version
// row.
type Announcement struct {
	ID        string
	RoomID    string
	Content   string
	Version   int
	CreatedBy string // account id of the poster
	CreatedAt time.Time
}

// AnnouncementRead is one per-account ack of a specific announcement
// version. Absence = unread (mirrors message_states semantics).
type AnnouncementRead struct {
	AnnouncementID string
	AccountID      string
	ReadAt         time.Time
}

// SystemAnnouncement is one global, admin-issued announcement (M6).
// Every account is implicitly unread until they ack via the
// system_announcement_reads table.
type SystemAnnouncement struct {
	ID        string
	Content   string
	CreatedBy string // admin account id
	CreatedAt time.Time
}

// SystemAnnouncementRead is one per-account ack of a system
// announcement. Absence = unread.
type SystemAnnouncementRead struct {
	SysAnnID  string
	AccountID string
	ReadAt    time.Time
}

// Attachment is one file attached to a persisted message (M7).
// LocalPath stays empty until the daemon's downloader finishes the
// inbound fetch (or, for outbound attachments, is set immediately to
// the path the caller uploaded from). DownloadedAt mirrors that
// availability — nil means "not yet on disk".
type Attachment struct {
	ID           string
	MessageID    string
	Filename     string
	Size         int64
	MIME         string
	LocalPath    string
	DiscordURL   string
	DownloadedAt *time.Time
	SHA256       string
	CreatedAt    time.Time
}
