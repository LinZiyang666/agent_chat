// Package v1 holds the HTTP request/response DTOs and handler
// functions for the agentchatd API at /v1/...
//
// Handlers depend on service interfaces from sibling business packages
// (account.Service, audit.Recorder, auth.Manager, store.* repos); they
// must not touch SQL or Discord directly.
package v1

import "time"

// ErrorBody is the inner body of every error response.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorEnvelope wraps ErrorBody so the JSON shape is { "error": {...} }.
// This matches the convention agreed in docs/03-architecture.md §6.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// CreateAccountRequest is the body of POST /v1/accounts.
type CreateAccountRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// UpdateAccountRequest is the body of PATCH /v1/accounts/{id}. All
// fields are optional; omitted fields are left unchanged.
type UpdateAccountRequest struct {
	Name *string `json:"name,omitempty"`
	Role *string `json:"role,omitempty"`
}

// AccountResponse is the public shape of an account.
type AccountResponse struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	LifecycleState string    `json:"lifecycle_state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AccountListResponse is the body of GET /v1/accounts.
type AccountListResponse struct {
	Accounts []AccountResponse `json:"accounts"`
}

// TokenResponse is the public shape of a token, without the secret.
type TokenResponse struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// CreateTokenResponse is the body of POST /v1/accounts/{id}/tokens.
// Raw contains the only copy of the plaintext token the server will
// ever emit; the caller must persist it on the spot.
type CreateTokenResponse struct {
	Token TokenResponse `json:"token"`
	Raw   string        `json:"raw"`
}

// TokenListResponse is the body of GET /v1/accounts/{id}/tokens.
type TokenListResponse struct {
	Tokens []TokenResponse `json:"tokens"`
}

// WhoamiResponse is the body of GET /v1/whoami.
type WhoamiResponse struct {
	Account AccountResponse `json:"account"`
	TokenID string          `json:"token_id"`
}

// AuditEntryResponse is the public shape of an audit row.
type AuditEntryResponse struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Action    string    `json:"action"`
	Target    string    `json:"target,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditListResponse is the body of GET /v1/audit.
type AuditListResponse struct {
	Entries []AuditEntryResponse `json:"entries"`
}

// CreateRoomRequest is the body of POST /v1/rooms. The caller's token
// determines the executor account (the bot that issues the
// CreateChannel call), so the request body does not name it.
type CreateRoomRequest struct {
	Name string `json:"name"`
}

// UpdateRoomRequest is the body of PATCH /v1/rooms/{id}. Optional
// fields are left unchanged when omitted.
type UpdateRoomRequest struct {
	Name *string `json:"name,omitempty"`
}

// RoomResponse is the public shape of a Room row.
type RoomResponse struct {
	ID               string    `json:"id"`
	DiscordChannelID string    `json:"discord_channel_id"`
	Name             string    `json:"name"`
	Archived         bool      `json:"archived"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// RoomListResponse is the body of GET /v1/rooms.
type RoomListResponse struct {
	Rooms []RoomResponse `json:"rooms"`
}

// InviteRequest is the body of POST /v1/rooms/{id}/members.
type InviteRequest struct {
	AccountID  string `json:"account_id"`
	Subscribed bool   `json:"subscribed"`
}

// MembershipResponse is the public shape of a Membership row.
type MembershipResponse struct {
	AccountID  string    `json:"account_id"`
	RoomID     string    `json:"room_id"`
	Subscribed bool      `json:"subscribed"`
	JoinedAt   time.Time `json:"joined_at"`
}

// MembershipListResponse is the body of GET /v1/rooms/{id}/members.
type MembershipListResponse struct {
	Members []MembershipResponse `json:"members"`
}

// UpdateMembershipRequest is the body of PATCH /v1/memberships/{room_id}.
// Used by user self-service subscribe/unsubscribe.
type UpdateMembershipRequest struct {
	Subscribed bool `json:"subscribed"`
}

// SendMessageRequest is the body of POST /v1/rooms/{id}/messages.
type SendMessageRequest struct {
	Content     string `json:"content"`
	ReplyToID   string `json:"reply_to_id,omitempty"`
	RequiresAck bool   `json:"requires_ack,omitempty"`
	Priority    string `json:"priority,omitempty"`
	// MentionAll (M6) signals @all: every member of the room sees the
	// message in their mentions feed, in addition to the literal
	// "<@bot_user_id>" content match. Off by default.
	MentionAll bool `json:"mention_all,omitempty"`
	// Attachments (M7) are local file paths the daemon reads and
	// uploads to Discord alongside the message. Discord caps each file
	// at 25 MB; oversize returns ATTACHMENT_TOO_LARGE.
	Attachments []SendAttachmentRequest `json:"attachments,omitempty"`
}

// SendAttachmentRequest is one outbound file in SendMessageRequest.
// Path is server-side (the daemon reads it). The CLI / agent is
// responsible for providing a path the daemon can resolve — the
// daemon does not do path translation.
type SendAttachmentRequest struct {
	Path     string `json:"path"`
	Filename string `json:"filename,omitempty"`
	MIME     string `json:"mime,omitempty"`
}

// MessageResponse is the public shape of a Message row.
type MessageResponse struct {
	ID              string               `json:"id"`
	RoomID          string               `json:"room_id"`
	AuthorAccountID string               `json:"author_account_id,omitempty"`
	DiscordMsgID    string               `json:"discord_msg_id"`
	Content         string               `json:"content"`
	ReplyToMsgID    string               `json:"reply_to_msg_id,omitempty"`
	RequiresAck     bool                 `json:"requires_ack"`
	Priority        string               `json:"priority"`
	CreatedAt       time.Time            `json:"created_at"`
	ContentHash     string               `json:"content_hash"`
	MentionAll      bool                 `json:"mention_all,omitempty"`
	Attachments     []AttachmentResponse `json:"attachments,omitempty"`
}

// AttachmentResponse is the public shape of an Attachment row (M7).
// LocalPath is empty until the downloader (inbound path) finishes
// fetching the file; outbound attachments populate it immediately.
type AttachmentResponse struct {
	ID           string     `json:"id"`
	MessageID    string     `json:"message_id"`
	Filename     string     `json:"filename"`
	Size         int64      `json:"size"`
	MIME         string     `json:"mime,omitempty"`
	LocalPath    string     `json:"local_path,omitempty"`
	DiscordURL   string     `json:"discord_url,omitempty"`
	DownloadedAt *time.Time `json:"downloaded_at,omitempty"`
	SHA256       string     `json:"sha256,omitempty"`
}

// MessageListResponse is the body of GET /v1/rooms/{id}/messages.
type MessageListResponse struct {
	Messages []MessageResponse `json:"messages"`
}

// MessageStateResponse is the public shape of a MessageState row.
type MessageStateResponse struct {
	MessageID string     `json:"message_id"`
	AccountID string     `json:"account_id"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	RepliedAt *time.Time `json:"replied_at,omitempty"`
}

// --- M6: announcements ----------------------------------------------------

// CreateAnnouncementRequest is the body of
// POST /v1/rooms/{id}/announcement. Posting bumps the version and
// resets unread for every current room member.
type CreateAnnouncementRequest struct {
	Content string `json:"content"`
}

// AnnouncementResponse is the public shape of an Announcement row.
type AnnouncementResponse struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	Content   string    `json:"content"`
	Version   int       `json:"version"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// Read is only populated by ListUnreadAnnouncements callers (always
	// false there) and by GetAnnouncement (true if the caller has ack'd).
	Read bool `json:"read"`
}

// AnnouncementReadResponse is the public shape of an
// AnnouncementRead row (M6).
type AnnouncementReadResponse struct {
	AnnouncementID string    `json:"announcement_id"`
	AccountID      string    `json:"account_id"`
	ReadAt         time.Time `json:"read_at"`
}

// CreateSystemAnnouncementRequest is the body of
// POST /v1/system/announcements.
type CreateSystemAnnouncementRequest struct {
	Content string `json:"content"`
}

// SystemAnnouncementResponse is the public shape of a
// SystemAnnouncement row.
type SystemAnnouncementResponse struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
}

// SystemAnnouncementListResponse is the body of
// GET /v1/system/announcements.
type SystemAnnouncementListResponse struct {
	Announcements []SystemAnnouncementResponse `json:"announcements"`
}

// SystemAnnouncementReadResponse is the public shape of a
// SystemAnnouncementRead row.
type SystemAnnouncementReadResponse struct {
	SysAnnID  string    `json:"sys_ann_id"`
	AccountID string    `json:"account_id"`
	ReadAt    time.Time `json:"read_at"`
}
