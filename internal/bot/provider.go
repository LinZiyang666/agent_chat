// Package bot defines the platform-agnostic Provider interface that
// abstracts a chat backend (Discord today, Matrix or Slack potentially
// later). All business code MUST depend on Provider, never on a
// concrete implementation — this is the load-bearing decoupling
// decision (docs/03-architecture.md D3.3).
//
// Concrete implementations live in subpackages:
//
//   - internal/bot/discord — discordgo-backed
//   - internal/bot/mock    — programmable mock for tests
package bot

import "context"

// Provider is the abstract chat-backend interface. Methods that
// produce side effects on the backend (SendMessage, CreateChannel,
// AddMember, …) take a context for cancellation; all I/O methods
// return errors that callers can inspect via errcode.As.
//
// Lifecycle contract:
//
//  1. Construct the Provider (no I/O).
//  2. Call Connect(ctx). Returns after the backend handshake completes
//     successfully (or returns an error). The first event emitted on
//     Events() afterwards is EventConnected.
//  3. Use the operational methods.
//  4. Call Disconnect(ctx) to release backend resources. The Events()
//     channel is closed after the final EventDisconnected.
//
// Implementations must be safe for concurrent use after Connect returns.
type Provider interface {
	// Connect performs the backend handshake. Errors are wrapped with
	// errcode codes the caller can branch on.
	Connect(ctx context.Context) error
	// Disconnect releases backend resources and closes Events().
	Disconnect(ctx context.Context) error
	// Status returns the current high-level connection state.
	Status() ConnStatus
	// Identity returns the bot's resolved identity. Valid only after
	// Connect has returned successfully.
	Identity() Identity

	// SendMessage delivers content to channelID. The returned Message
	// echoes the persisted form (with platform-assigned id).
	SendMessage(ctx context.Context, channelID, content string, opts SendOptions) (*Message, error)

	// CreateChannel asks the backend to create a new text channel.
	// Returns the platform-assigned channel id. M3 may stub this; M4
	// will exercise it from the room subsystem.
	CreateChannel(ctx context.Context, name string) (channelID string, err error)
	// DeleteChannel removes a channel.
	DeleteChannel(ctx context.Context, channelID string) error

	// AddMember grants memberID access to channelID. For Discord-like
	// platforms this typically maps to a per-channel permission
	// override; for Matrix-like platforms to a room invite.
	AddMember(ctx context.Context, channelID, memberID string) error
	// RemoveMember revokes that access.
	RemoveMember(ctx context.Context, channelID, memberID string) error

	// FetchHistory returns up to limit messages from channelID, ordered
	// oldest-first. beforeID, if non-empty, fetches messages strictly
	// older than beforeID. limit <= 0 is treated as the backend default.
	FetchHistory(ctx context.Context, channelID, beforeID string, limit int) ([]Message, error)

	// Events returns a read-only channel of normalized events. The
	// channel is closed when the Provider disconnects.
	Events() <-chan Event
}
