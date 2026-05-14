package bot

import "time"

// ConnStatus is a high-level summary of a Provider's connection state.
// Concrete implementations may have richer internal states; this enum
// is what the rest of the system sees.
type ConnStatus string

const (
	// StatusOffline means the Provider is intentionally not connected.
	StatusOffline ConnStatus = "offline"
	// StatusConnecting means Connect has been called but the handshake
	// with the backend is still in progress.
	StatusConnecting ConnStatus = "connecting"
	// StatusOnline means the Provider is fully connected and exchanging
	// events with the backend.
	StatusOnline ConnStatus = "online"
	// StatusErrored means a previous Connect succeeded but the
	// connection has since dropped and the Provider failed to recover.
	// Disconnect should still be called to release resources.
	StatusErrored ConnStatus = "errored"
)

// Identity is the public-facing identity a Provider exposes on its
// backend. UserID is platform-specific (Discord snowflake, Matrix MXID,
// …); callers must not parse it.
type Identity struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// Message is a normalized inbound or outbound chat message. M3 keeps
// the field set minimal; mentions, replies, and attachments grow in M4.
type Message struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	AuthorID  string    `json:"author_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// SendOptions is the extensible bag of optional parameters for
// SendMessage. M3 keeps this minimal; M4 will grow reply, mentions,
// and attachments.
type SendOptions struct {
	// ReplyToMessageID, if set, marks the new message as a reply to
	// the given platform message ID.
	ReplyToMessageID string
}
