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

// Message is a normalized inbound or outbound chat message.
type Message struct {
	ID          string         `json:"id"`
	ChannelID   string         `json:"channel_id"`
	AuthorID    string         `json:"author_id"`
	Content     string         `json:"content"`
	CreatedAt   time.Time      `json:"created_at"`
	Attachments []MsgAttachURL `json:"attachments,omitempty"`
}

// MsgAttachURL describes an attachment as the bot Provider sees it
// during ingestion: an external URL (Discord CDN), a filename, and
// size. The downloader subscribes to message events, sees these
// rows, and fetches the bytes into the local cache. The agentchat
// `Attachment` store row (with `local_path` + `sha256`) is a
// downstream side effect; this bot-side struct stays small and
// platform-shaped.
type MsgAttachURL struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	MIME     string `json:"mime,omitempty"`
}

// UploadFile describes one local file the send path wants to upload
// as an attachment alongside an outbound message. The Discord
// adapter reads the bytes from Path and posts the multipart payload.
// FileName overrides the on-Discord display name (defaults to
// filepath.Base(Path) when empty). MIME is optional — the adapter
// will sniff if unset.
type UploadFile struct {
	Path     string
	FileName string
	MIME     string
}

// SendOptions is the extensible bag of optional parameters for
// SendMessage.
type SendOptions struct {
	// ReplyToMessageID, if set, marks the new message as a reply to
	// the given platform message ID.
	ReplyToMessageID string
	// Attachments are local files to upload. Discord caps each file
	// at 25 MB; callers are responsible for the size check (the API
	// layer maps the failure mode to `ATTACHMENT_TOO_LARGE`).
	Attachments []UploadFile
}
