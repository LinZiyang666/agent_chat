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

	// 4. Priority feed: urgent + system messages the agent has not
	// yet read. (M9 Phase 2 retired the requires_ack / replied_at
	// "PendingAcks" dimension — @-mentions now subsume that signal:
	// being @-mentioned IS the "you need to deal with this" hint.)
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

	// M6 additions — unread announcements (room-scoped) and unread
	// system announcements. Each is a separate dimension because the
	// state UI surfaces them differently from regular messages
	// (mandatory-read semantics). The visible feed is capped; counts
	// in Totals reflect the full unbounded set.

	// Announcements is the list of unread room announcements that
	// concern the agent (= latest version in every room they are a
	// member of, with no ack row yet).
	Announcements []AnnouncementEntry `json:"announcements"`

	// SystemAnnouncements is the list of unread system announcements.
	SystemAnnouncements []SystemAnnouncementEntry `json:"system_announcements"`
}

// Totals is the aggregate counter block (dimension 1).
type Totals struct {
	Unread   int `json:"unread"`
	Mentions int `json:"mentions"`
	Priority int `json:"priority"`
	// M6: unread announcement counts. Announcements is the count of
	// rooms the agent is a member of whose latest announcement hasn't
	// been ack'd yet. SystemAnnouncements is the count of system
	// announcements without a per-account ack row.
	Announcements       int `json:"announcements"`
	SystemAnnouncements int `json:"system_announcements"`
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
//
// The JSON key for the row identifier is `id` (M6-P3 normalization)
// to match the messages API's `MessageResponse.ID`. M5 originally
// emitted `message_id`; consumers updated during the M6 audit pass.
type MessageEntry struct {
	MessageID       string    `json:"id"`
	RoomID          string    `json:"room_id"`
	RoomName        string    `json:"room_name"`
	AuthorAccountID string    `json:"author_account_id,omitempty"`
	Priority        string    `json:"priority"`
	Content         string    `json:"content"`
	CreatedAt       time.Time `json:"created_at"`
}

// AnnouncementEntry is the slim view of a room announcement used by
// the M6 announcements dimension. Like MessageEntry, callers can
// re-fetch via GET /v1/rooms/{id}/announcement for the full row.
type AnnouncementEntry struct {
	AnnouncementID string    `json:"announcement_id"`
	RoomID         string    `json:"room_id"`
	RoomName       string    `json:"room_name"`
	Version        int       `json:"version"`
	Content        string    `json:"content"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

// SystemAnnouncementEntry is the slim view of a system announcement.
type SystemAnnouncementEntry struct {
	SysAnnID  string    `json:"sys_ann_id"`
	Content   string    `json:"content"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
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
