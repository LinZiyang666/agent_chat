package state

import "time"

// Snapshot is the per-account state-view envelope agents subscribe
// to. It captures the 8 dimensions from requirements §5.2 plus a
// monotonic version.
//
// The version is a daemon-process-wide counter useful for "did I
// miss a frame?" detection by a reconnecting client (compare to the
// last version observed). It is NOT a per-account replay cursor —
// the bus has no event history, so it cannot replay missed frames.
// Roadmap §6's `?since=<version>` resume semantics is explicitly
// deferred to M8 per M5-P3-005 audit decision; the watch endpoint
// rejects an explicit since= query for now to avoid the silent
// "looks like it works" trap.
type Snapshot struct {
	// Version is the per-process monotonic counter the bus assigns
	// (atomic.Int64). Strictly increasing across all subscribers in
	// the daemon, not per-account. See type-level doc above for
	// what it does NOT mean.
	Version int64 `json:"version"`
	// AccountID is the agent the snapshot was built for.
	AccountID string `json:"account_id"`
	// EmittedAt is the wall-clock time the snapshot was assembled.
	EmittedAt time.Time `json:"emitted_at"`

	// 1. Totals: aggregate counters across the subscribed rooms.
	Totals Totals `json:"totals"`

	// 2. Per-room unread counts (subscribed rooms only).
	Rooms []RoomUnread `json:"rooms"`

	// 3. @-me messages the agent has not yet read.
	Mentions []MessageEntry `json:"mentions"`

	// 4. Messages the agent owes a reply on (requires_ack=true and
	// the agent's own replied_at is still null).
	PendingAcks []MessageEntry `json:"pending_acks"`

	// 5. Priority feed: urgent + system messages the agent has not
	// yet read.
	Priority []MessageEntry `json:"priority"`

	// 6. New rooms: rooms the agent has joined recently. M5 ships
	// without the "required announcement" attachment (announcements
	// arrive in M6). The dimension still surfaces the room metadata
	// so the state UI can render the "new" badge.
	NewRooms []RoomEntry `json:"new_rooms"`

	// 7. Recently active rooms (subscribed), ordered by the
	// timestamp of the latest message known to agentchat.
	RecentlyActive []RoomEntry `json:"recently_active"`

	// 8. Health bar — token / Discord API / network / recent errors.
	Health Health `json:"health"`
}

// Totals is the aggregate counter block (dimension 1).
type Totals struct {
	Unread      int `json:"unread"`
	Mentions    int `json:"mentions"`
	PendingAcks int `json:"pending_acks"`
	Priority    int `json:"priority"`
}

// RoomUnread is one row of the per-room unread breakdown
// (dimension 2). Subscribed rooms only.
type RoomUnread struct {
	RoomID string `json:"room_id"`
	Name   string `json:"name"`
	Unread int    `json:"unread"`
}

// RoomEntry is one room descriptor used by NewRooms and
// RecentlyActive. LastMessageAt is the agentchat-visible last
// message; nil if the local store hasn't seen any message for the
// room yet.
type RoomEntry struct {
	RoomID        string     `json:"room_id"`
	Name          string     `json:"name"`
	Subscribed    bool       `json:"subscribed"`
	JoinedAt      time.Time  `json:"joined_at"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
	LastMessageID string     `json:"last_message_id,omitempty"`
}

// MessageEntry is the slim view of a message used by the mentions /
// pending-ack / priority sections. The state UI keeps these short;
// callers can re-fetch via /v1/rooms/{id}/messages when they want
// the full body.
type MessageEntry struct {
	MessageID       string    `json:"message_id"`
	RoomID          string    `json:"room_id"`
	RoomName        string    `json:"room_name"`
	AuthorAccountID string    `json:"author_account_id,omitempty"`
	Priority        string    `json:"priority"`
	RequiresAck     bool      `json:"requires_ack"`
	Content         string    `json:"content"`
	CreatedAt       time.Time `json:"created_at"`
}

// Health is the system-health bar (dimension 8). M5 wires the
// token + provider columns; network and recent_errors stay
// placeholder until M8 supplies the proper instrumentation.
type Health struct {
	// TokenOK reports whether the calling token is still valid (we
	// only build snapshots for tokens that pass auth, so this is
	// always true at emission time, kept for forward-compat).
	TokenOK bool `json:"token_ok"`
	// ProviderStatus is the agent's own bot connection status (M3
	// vocabulary: offline / connecting / online / errored). Empty
	// when the agent has no bot configured at all.
	ProviderStatus string `json:"provider_status"`
	// DiscordReachable is a best-effort flag derived from
	// ProviderStatus — true iff status == "online".
	DiscordReachable bool `json:"discord_reachable"`
	// RecentErrors carries the last few daemon-side errors that
	// concern this account. Empty in M5; M8 will populate.
	RecentErrors []string `json:"recent_errors,omitempty"`
}
