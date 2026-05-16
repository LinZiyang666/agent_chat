package bot

import (
	"context"
	"time"
)

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
	// MentionedBotUserIDs is the list of platform user IDs the message
	// directly @-mentions (M9). Discord populates this from the
	// `MESSAGE_CREATE.mentions` array; non-Discord providers may
	// leave it nil. Daemon ingester maps these to account IDs via
	// accounts.bot_user_id.
	MentionedBotUserIDs []string `json:"mentioned_bot_user_ids,omitempty"`
	// MentionEveryone is true when the message includes an @everyone
	// (Discord `MESSAGE_CREATE.mention_everyone`). Replaces the M6
	// `--all` flag in M9.
	MentionEveryone bool `json:"mention_everyone,omitempty"`
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

// IdentityProber is the bot-layer seam the daemon uses to check a
// candidate bot token against the platform BEFORE persisting it (M9
// Phase 2 set-discord verification). Implementations:
//
//   - internal/bot/discord.Prober: hits Discord REST GET /users/@me
//     with `Bot <token>` to fetch the real username + snowflake.
//   - internal/bot/mock.Prober:    derives identity from the hint
//     (Username = hint.Username, UserID = "u-"+hint.Username) so the
//     existing mock-driven test suite keeps passing.
//
// Both implementations are stateless and safe to call once per
// set-discord request. The Discord one does NOT open a gateway
// WebSocket — only the REST endpoint, so the call is light.
type IdentityProber interface {
	// Probe returns the platform identity that `token` resolves to.
	// `hint` carries the agentchat account name (used by mocks to
	// synthesize a deterministic identity); platform implementations
	// may ignore it. An error from Probe is fatal to the calling
	// request — the daemon should NOT persist a token it couldn't
	// confirm. Wrap network/auth failures with errcode.Unavailable
	// or AuthInvalid so the API layer can map them sensibly.
	Probe(ctx context.Context, token string, hint Identity) (Identity, error)
	// Rename changes the platform-side username of the bot the token
	// owns to `newUsername`. Used by set-discord's `--force-rename`
	// path when the configured account name and the bot's Discord
	// username don't match. Implementations should map rate-limit
	// failures to a recognisable error (Discord caps bot username
	// changes at 2/h) so the caller can surface a retry hint.
	Rename(ctx context.Context, token string, newUsername string) error
}

// SendOptions is the extensible bag of optional parameters for
// SendMessage.
type SendOptions struct {
	// ReplyToMessageID, if set, marks the new message as a reply to
	// the given platform message ID.
	ReplyToMessageID string
	// Attachments are local files to upload. Discord caps each file
	// at 10 MB on the free tier (see internal/api/v1/messages.go
	// DiscordAttachmentLimit); callers are responsible for the size
	// check, the API layer maps oversize to `ATTACHMENT_TOO_LARGE`.
	Attachments []UploadFile
	// MentionAllowedUserIDs is the deduplicated list of platform user
	// IDs the daemon has authorized this message to ping (M9 Phase
	// 2). The send-path handler builds this by calling
	// bot.ParseMentions(content, members) against the room's current
	// member set, so only members named via @<name> in the request
	// end up here. The Discord adapter passes it straight to
	// `discordgo.MessageAllowedMentions.Users` — anything in content
	// that is NOT on this list (e.g. a stray @everyone literal) will
	// be displayed but will not ping.
	MentionAllowedUserIDs []string
	// MentionAllowedEveryone is true when the daemon parser flagged
	// the content as containing `@everyone` and authorized it. The
	// Discord adapter maps this to AllowedMentionTypeEveryone.
	MentionAllowedEveryone bool
}
