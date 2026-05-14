package state

import (
	"context"
	"sort"
	"time"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// ProviderStatusFn is the seam the aggregator uses to read an
// account's bot connection status. In production this is
// (*connector.Connector).Status; tests can inject a stub.
type ProviderStatusFn func(accountID string) bot.ConnStatus

// Aggregator assembles per-account Snapshots. It is read-only and
// stateless beyond its dependencies — the per-account snapshots are
// recomputed on each Build call. Caching/debouncing lives in the bus.
type Aggregator struct {
	bundle store.Bundle
	status ProviderStatusFn
	now    func() time.Time

	// Tunables (capped sizes for the three message lists).
	mentionsLimit int
	pendingLimit  int
	priorityLimit int
	newRoomsLimit int
	recentLimit   int
	// newRoomsWindow is the lookback for the NewRooms dimension —
	// memberships joined within this duration count as "new".
	newRoomsWindow time.Duration
}

// New constructs an Aggregator. Pass conn.Status (or a stub for
// tests) so the health dimension can report a sensible provider
// state.
func New(bundle store.Bundle, status ProviderStatusFn) *Aggregator {
	if status == nil {
		status = func(string) bot.ConnStatus { return bot.StatusOffline }
	}
	return &Aggregator{
		bundle:         bundle,
		status:         status,
		now:            func() time.Time { return time.Now().UTC() },
		mentionsLimit:  50,
		pendingLimit:   50,
		priorityLimit:  50,
		newRoomsLimit:  5,
		recentLimit:    20,
		newRoomsWindow: 24 * time.Hour,
	}
}

// NewFromConnector is the production wiring helper.
func NewFromConnector(bundle store.Bundle, conn *connector.Connector) *Aggregator {
	return New(bundle, conn.Status)
}

// SetClock overrides the time source. Tests only.
func (a *Aggregator) SetClock(now func() time.Time) { a.now = now }

// Build assembles a Snapshot for accountID. The version is set by
// the caller (typically the bus's atomic counter) so the snapshot
// can be addressed without further mutation.
func (a *Aggregator) Build(ctx context.Context, accountID string, version int64) (*Snapshot, error) {
	if accountID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "account id is empty")
	}

	account, err := a.bundle.Accounts.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}

	// One joined query in place of N+1 Rooms.Get calls (M5-P3-006).
	joined, err := a.bundle.Memberships.ListByAccountWithRooms(ctx, accountID)
	if err != nil {
		return nil, err
	}
	roomByID := make(map[string]*store.Room, len(joined))
	for _, mr := range joined {
		roomByID[mr.Membership.RoomID] = mr.Room
	}

	// Dimension 1: totals — true aggregate counts, NOT capped by
	// the feed limits (M5-P3-002 fix). All four counters are scoped
	// to subscribed + non-archived rooms (M5-P3-001 fix).
	totalsUnread, err := a.bundle.MessageStates.CountUnreadForSubscribed(ctx, accountID)
	if err != nil {
		return nil, err
	}
	totalsMentions, err := a.bundle.MessageStates.CountMentionsForSubscribed(ctx, accountID, account.BotUserID)
	if err != nil {
		return nil, err
	}
	totalsPendingAcks, err := a.bundle.MessageStates.CountPendingAcksForSubscribed(ctx, accountID)
	if err != nil {
		return nil, err
	}
	totalsPriority, err := a.bundle.MessageStates.CountPriorityForSubscribed(ctx, accountID)
	if err != nil {
		return nil, err
	}

	// Dimension 2: per-room unread for subscribed non-archived rooms.
	perRoomUnread, err := a.bundle.MessageStates.UnreadCountByRoomForSubscribed(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rooms := make([]RoomUnread, 0, len(joined))
	for _, mr := range joined {
		if !mr.Membership.Subscribed || mr.Room.Archived {
			continue
		}
		rooms = append(rooms, RoomUnread{
			RoomID: mr.Membership.RoomID,
			Name:   mr.Room.Name,
			Unread: perRoomUnread[mr.Membership.RoomID],
		})
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })

	// Dimensions 3/4/5: capped feeds. Each repo call is already
	// subscribed-AND-non-archived scoped (M5-P3-001) and capped at
	// `limit` rows (the Counts above are the unbounded source of
	// truth for Totals).
	mentions, err := a.listEntries(ctx, roomByID, func(c context.Context) ([]*store.Message, error) {
		return a.bundle.MessageStates.ListMentionsForSubscribed(c, accountID, account.BotUserID, a.mentionsLimit)
	})
	if err != nil {
		return nil, err
	}
	pendingAcks, err := a.listEntries(ctx, roomByID, func(c context.Context) ([]*store.Message, error) {
		return a.bundle.MessageStates.ListPendingAcksForSubscribed(c, accountID, a.pendingLimit)
	})
	if err != nil {
		return nil, err
	}
	priority, err := a.listEntries(ctx, roomByID, func(c context.Context) ([]*store.Message, error) {
		return a.bundle.MessageStates.ListPriorityForSubscribed(c, accountID, a.priorityLimit)
	})
	if err != nil {
		return nil, err
	}

	// Dimensions 6+7: new rooms + recently active.
	latestPerRoom, err := a.bundle.Messages.LatestPerRoomForMember(ctx, accountID)
	if err != nil {
		return nil, err
	}
	newRooms, recent := a.buildRoomFeeds(joined, latestPerRoom)

	// Dimension 8: health bar.
	health := Health{
		TokenOK: true,
	}
	if len(account.BotTokenEnc) > 0 {
		st := string(a.status(accountID))
		health.ProviderStatus = st
		health.DiscordReachable = st == string(bot.StatusOnline)
	}

	return &Snapshot{
		Version:   version,
		AccountID: accountID,
		EmittedAt: a.now(),
		Totals: Totals{
			Unread:      totalsUnread,
			Mentions:    totalsMentions,
			PendingAcks: totalsPendingAcks,
			Priority:    totalsPriority,
		},
		Rooms:          rooms,
		Mentions:       mentions,
		PendingAcks:    pendingAcks,
		Priority:       priority,
		NewRooms:       newRooms,
		RecentlyActive: recent,
		Health:         health,
	}, nil
}

func (a *Aggregator) listEntries(ctx context.Context, roomByID map[string]*store.Room,
	q func(context.Context) ([]*store.Message, error),
) ([]MessageEntry, error) {
	msgs, err := q(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MessageEntry, 0, len(msgs))
	for _, m := range msgs {
		entry := MessageEntry{
			MessageID:       m.ID,
			RoomID:          m.RoomID,
			AuthorAccountID: m.AuthorAccountID,
			Priority:        string(m.Priority),
			RequiresAck:     m.RequiresAck,
			Content:         m.Content,
			CreatedAt:       m.CreatedAt,
		}
		if room := roomByID[m.RoomID]; room != nil {
			entry.RoomName = room.Name
		}
		out = append(out, entry)
	}
	return out, nil
}

func (a *Aggregator) buildRoomFeeds(
	joined []*store.MembershipWithRoom,
	latest map[string]*store.Message,
) (newRooms, recent []RoomEntry) {
	now := a.now()
	cutoff := now.Add(-a.newRoomsWindow)

	// Build all entries first, then partition / sort / cap.
	all := make([]RoomEntry, 0, len(joined))
	for _, mr := range joined {
		if mr.Room.Archived {
			continue
		}
		entry := RoomEntry{
			RoomID:     mr.Membership.RoomID,
			Name:       mr.Room.Name,
			Subscribed: mr.Membership.Subscribed,
			JoinedAt:   mr.Membership.JoinedAt,
		}
		if msg := latest[mr.Membership.RoomID]; msg != nil {
			t := msg.CreatedAt
			entry.LastMessageAt = &t
			entry.LastMessageID = msg.ID
		}
		all = append(all, entry)
	}

	// NewRooms: subscribed memberships joined within the cutoff
	// window, newest first, capped at newRoomsLimit.
	//
	// Primary state ONLY (M5-P3-003 fix): per requirements §4 and
	// §5.2.1, "属 + 已订阅" is primary; "属 + 未订阅" (旁观) is
	// secondary state. NewRooms is a primary dimension, so we
	// must exclude unsubscribed (旁观) fresh memberships here. They
	// surface later through the secondary endpoint (M7+).
	newRooms = make([]RoomEntry, 0)
	for _, e := range all {
		if !e.Subscribed {
			continue
		}
		if e.JoinedAt.After(cutoff) {
			newRooms = append(newRooms, e)
		}
	}
	sort.Slice(newRooms, func(i, j int) bool {
		return newRooms[i].JoinedAt.After(newRooms[j].JoinedAt)
	})
	if len(newRooms) > a.newRoomsLimit {
		newRooms = newRooms[:a.newRoomsLimit]
	}

	// RecentlyActive: subscribed rooms ordered by latest_message_at
	// (rooms without a known last message land at the bottom by
	// joined_at).
	recent = make([]RoomEntry, 0)
	for _, e := range all {
		if !e.Subscribed {
			continue
		}
		recent = append(recent, e)
	}
	sort.Slice(recent, func(i, j int) bool {
		li, lj := recent[i].LastMessageAt, recent[j].LastMessageAt
		switch {
		case li != nil && lj != nil:
			return li.After(*lj)
		case li != nil:
			return true
		case lj != nil:
			return false
		default:
			return recent[i].JoinedAt.After(recent[j].JoinedAt)
		}
	})
	if len(recent) > a.recentLimit {
		recent = recent[:a.recentLimit]
	}
	return newRooms, recent
}
