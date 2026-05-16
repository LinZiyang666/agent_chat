package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// fx is the per-test sqlite + Aggregator + seed helpers harness.
type fx struct {
	t      *testing.T
	store  *sqlite.Store
	agg    *Aggregator
	status bot.ConnStatus
}

func newFx(t *testing.T) *fx {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	f := &fx{t: t, store: s, status: bot.StatusOnline}
	f.agg = New(s.Bundle(), func(_ string) bot.ConnStatus { return f.status })
	return f
}

func (f *fx) build(accountID string) *Snapshot {
	snap, err := f.agg.Build(context.Background(), accountID, 1)
	require.NoError(f.t, err)
	return snap
}

func (f *fx) account(name, botUserID string) *store.Account {
	now := time.Now().UTC()
	id, err := uuid.NewV7()
	require.NoError(f.t, err)
	a := &store.Account{
		ID:             id.String(),
		Name:           name,
		Role:           store.RoleUser,
		LifecycleState: store.LifecycleOnline,
		BotUserID:      botUserID,
		BotTokenEnc:    []byte("dummy"),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(f.t, f.store.Bundle().Accounts.Create(context.Background(), a))
	return a
}

func (f *fx) room(name string) *store.Room {
	now := time.Now().UTC()
	id, err := uuid.NewV7()
	require.NoError(f.t, err)
	r := &store.Room{
		ID:               id.String(),
		DiscordChannelID: "ch-" + name,
		Name:             name,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(f.t, f.store.Bundle().Rooms.Create(context.Background(), r))
	return r
}

func (f *fx) joinedAt(acc, room string, subscribed bool, joinedAt time.Time) {
	require.NoError(f.t, f.store.Bundle().Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: acc, RoomID: room, Subscribed: subscribed, JoinedAt: joinedAt,
	}))
}

func (f *fx) member(acc, room string, subscribed bool) {
	f.joinedAt(acc, room, subscribed, time.Now().UTC())
}

func (f *fx) message(room string, content string, opts ...func(*store.Message)) *store.Message {
	now := time.Now().UTC()
	id, err := uuid.NewV7()
	require.NoError(f.t, err)
	m := &store.Message{
		ID:           id.String(),
		RoomID:       room,
		DiscordMsgID: "dm-" + id.String(),
		Content:      content,
		Priority:     store.PriorityNormal,
		CreatedAt:    now,
		ContentHash:  "h",
	}
	for _, fn := range opts {
		fn(m)
	}
	require.NoError(f.t, f.store.Bundle().Messages.Create(context.Background(), m))
	return m
}

func (f *fx) state(msg, acc string, readAt, repliedAt *time.Time) {
	require.NoError(f.t, f.store.Bundle().MessageStates.Upsert(context.Background(), &store.MessageState{
		MessageID: msg, AccountID: acc, ReadAt: readAt, RepliedAt: repliedAt,
	}))
}

// mention writes a (msg_id, account_id) row into message_mentions,
// simulating the ingester's resolution of Discord MESSAGE_CREATE.mentions
// for the account (M9 Phase 1). Use this in tests that need to assert
// mention-feed behaviour without going through the full Discord event
// pipeline.
func (f *fx) mention(msgID string, accountIDs ...string) {
	require.NoError(f.t, f.store.Bundle().MessageMentions.SetForMessage(
		context.Background(), msgID, accountIDs))
}

func TestEmptySnapshotShape(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")

	snap := f.build(viewer.ID)
	assert.Equal(t, viewer.ID, snap.AccountID)
	assert.Equal(t, int64(1), snap.Version)
	assert.Equal(t, 0, snap.Totals.Unread)
	assert.Empty(t, snap.Rooms)
	assert.Empty(t, snap.Mentions)
	assert.Empty(t, snap.PendingAcks)
	assert.Empty(t, snap.Priority)
	assert.Empty(t, snap.NewRooms)
	assert.Empty(t, snap.RecentlyActive)
	assert.True(t, snap.Health.TokenOK)
	assert.Equal(t, "online", snap.Health.ProviderStatus)
}

func TestSnapshotCountsUnreadOnlyForSubscribed(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	rSubbed := f.room("subbed")
	rLurker := f.room("lurker")
	f.member(viewer.ID, rSubbed.ID, true)
	f.member(viewer.ID, rLurker.ID, false)

	m1 := f.message(rSubbed.ID, "in subbed")
	m2 := f.message(rLurker.ID, "in lurker")
	f.state(m1.ID, viewer.ID, nil, nil)
	f.state(m2.ID, viewer.ID, nil, nil)

	snap := f.build(viewer.ID)
	assert.Equal(t, 1, snap.Totals.Unread, "lurker (subscribed=false) must not contribute to totals.unread")
	require.Len(t, snap.Rooms, 1)
	assert.Equal(t, rSubbed.ID, snap.Rooms[0].RoomID)
	assert.Equal(t, 1, snap.Rooms[0].Unread)
}

// M9 Phase 1: mentions now come from the message_mentions table, not
// from content `<@bot_user_id>` substring matching. The test fixture's
// `mention()` helper writes the same row the ingester would write after
// resolving Discord MESSAGE_CREATE.mentions[] via accounts.bot_user_id.
func TestSnapshotMentionsFromMessageMentions(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("r")
	f.member(viewer.ID, r.ID, true)
	mentionMsg := f.message(r.ID, "hey please look")
	noMention := f.message(r.ID, "general chatter")
	f.mention(mentionMsg.ID, viewer.ID)
	f.state(mentionMsg.ID, viewer.ID, nil, nil)
	f.state(noMention.ID, viewer.ID, nil, nil)

	snap := f.build(viewer.ID)
	require.Len(t, snap.Mentions, 1)
	assert.Equal(t, mentionMsg.ID, snap.Mentions[0].MessageID)
	assert.Equal(t, 1, snap.Totals.Mentions)
}

// Mention rows are scoped per (message, account); a row pointing at
// someone else must NOT surface in this viewer's mention feed.
func TestSnapshotMentionsAccountScoped(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	other := f.account("other", "u-other")
	r := f.room("r")
	f.member(viewer.ID, r.ID, true)
	f.member(other.ID, r.ID, true)
	m := f.message(r.ID, "talking to other")
	f.mention(m.ID, other.ID)
	f.state(m.ID, viewer.ID, nil, nil)
	f.state(m.ID, other.ID, nil, nil)

	snap := f.build(viewer.ID)
	assert.Empty(t, snap.Mentions, "viewer was not in the mention set")
	assert.Equal(t, 0, snap.Totals.Mentions)
}

// M9: a message with mention_everyone=1 mentions every member of the
// room regardless of subscription state (preserves M6-P3-001 semantic).
func TestSnapshotMentionsEveryoneCrossesSubscribed(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("r")
	f.member(viewer.ID, r.ID, false) // 旁观 (unsubscribed)
	m := f.message(r.ID, "all hands", func(m *store.Message) {
		m.MentionEveryone = true
	})
	f.state(m.ID, viewer.ID, nil, nil)

	snap := f.build(viewer.ID)
	require.Len(t, snap.Mentions, 1, "@everyone reaches even unsubscribed members")
	assert.Equal(t, m.ID, snap.Mentions[0].MessageID)
	assert.Equal(t, 1, snap.Totals.Mentions)
}

// Per-user @ mentions stay subscribed-only — a targeted ping to an
// unsubscribed (旁观) member must NOT promote them to the active state
// UI.
func TestSnapshotPerUserMentionRequiresSubscribed(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("r")
	f.member(viewer.ID, r.ID, false)
	m := f.message(r.ID, "hey")
	f.mention(m.ID, viewer.ID)
	f.state(m.ID, viewer.ID, nil, nil)

	snap := f.build(viewer.ID)
	assert.Empty(t, snap.Mentions, "per-user mention to unsubscribed member must not surface")
	assert.Equal(t, 0, snap.Totals.Mentions)
}

func TestSnapshotPendingAcks(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("r")
	f.member(viewer.ID, r.ID, true)
	ackable := f.message(r.ID, "please ack", func(m *store.Message) { m.RequiresAck = true })
	normal := f.message(r.ID, "no ack needed")
	read := time.Now().UTC()
	// Read but not yet replied — still pending.
	f.state(ackable.ID, viewer.ID, &read, nil)
	f.state(normal.ID, viewer.ID, &read, nil)

	snap := f.build(viewer.ID)
	require.Len(t, snap.PendingAcks, 1)
	assert.Equal(t, ackable.ID, snap.PendingAcks[0].MessageID)
}

func TestSnapshotPendingAcksClearsWhenReplied(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("r")
	f.member(viewer.ID, r.ID, true)
	m := f.message(r.ID, "ack", func(m *store.Message) { m.RequiresAck = true })
	now := time.Now().UTC()
	f.state(m.ID, viewer.ID, &now, &now)

	snap := f.build(viewer.ID)
	assert.Empty(t, snap.PendingAcks)
}

func TestSnapshotPriorityListsUrgentAndSystem(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("r")
	f.member(viewer.ID, r.ID, true)
	urgent := f.message(r.ID, "fire!", func(m *store.Message) { m.Priority = store.PriorityUrgent })
	sys := f.message(r.ID, "maintenance window", func(m *store.Message) { m.Priority = store.PrioritySystem })
	plain := f.message(r.ID, "hello")
	f.state(urgent.ID, viewer.ID, nil, nil)
	f.state(sys.ID, viewer.ID, nil, nil)
	f.state(plain.ID, viewer.ID, nil, nil)

	snap := f.build(viewer.ID)
	assert.Len(t, snap.Priority, 2)
}

func TestSnapshotNewRoomsWithinWindow(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	rOld := f.room("old")
	rFresh := f.room("fresh")
	f.joinedAt(viewer.ID, rOld.ID, true, time.Now().Add(-48*time.Hour).UTC())
	f.joinedAt(viewer.ID, rFresh.ID, true, time.Now().Add(-1*time.Hour).UTC())

	snap := f.build(viewer.ID)
	require.Len(t, snap.NewRooms, 1)
	assert.Equal(t, rFresh.ID, snap.NewRooms[0].RoomID)
}

func TestSnapshotRecentlyActiveOrderedByLastMessage(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	rA := f.room("a")
	rB := f.room("b")
	f.member(viewer.ID, rA.ID, true)
	f.member(viewer.ID, rB.ID, true)

	// rA gets an older message; rB gets a newer one.
	mA := f.message(rA.ID, "old", func(m *store.Message) {
		m.CreatedAt = time.Now().Add(-1 * time.Hour).UTC()
	})
	mB := f.message(rB.ID, "new", func(m *store.Message) {
		m.CreatedAt = time.Now().UTC()
	})
	_ = mA
	_ = mB

	snap := f.build(viewer.ID)
	require.Len(t, snap.RecentlyActive, 2)
	assert.Equal(t, rB.ID, snap.RecentlyActive[0].RoomID, "newest-active first")
	assert.Equal(t, rA.ID, snap.RecentlyActive[1].RoomID)
}

func TestSnapshotHealthReflectsProviderStatus(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	f.status = bot.StatusErrored

	snap := f.build(viewer.ID)
	assert.Equal(t, "errored", snap.Health.ProviderStatus)
	assert.False(t, snap.Health.DiscordReachable)
}

func TestSnapshotArchivedRoomsExcluded(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("alive")
	r.Archived = true
	r.UpdatedAt = time.Now().UTC()
	require.NoError(t, f.store.Bundle().Rooms.Update(context.Background(), r))
	f.member(viewer.ID, r.ID, true)

	snap := f.build(viewer.ID)
	assert.Empty(t, snap.Rooms)
	assert.Empty(t, snap.RecentlyActive)
}

func TestPhase3ArchivedRoomsDoNotLeakIntoPrimaryState(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("archived")
	f.member(viewer.ID, r.ID, true)
	m := f.message(r.ID, "urgent ack <@u-viewer>", func(m *store.Message) {
		m.RequiresAck = true
		m.Priority = store.PriorityUrgent
	})
	f.state(m.ID, viewer.ID, nil, nil)

	r.Archived = true
	r.UpdatedAt = time.Now().UTC()
	require.NoError(t, f.store.Bundle().Rooms.Update(context.Background(), r))

	snap := f.build(viewer.ID)
	assert.Equal(t, 0, snap.Totals.Unread)
	assert.Equal(t, 0, snap.Totals.Mentions)
	assert.Equal(t, 0, snap.Totals.PendingAcks)
	assert.Equal(t, 0, snap.Totals.Priority)
	assert.Empty(t, snap.Rooms)
	assert.Empty(t, snap.Mentions)
	assert.Empty(t, snap.PendingAcks)
	assert.Empty(t, snap.Priority)
	assert.Empty(t, snap.NewRooms)
	assert.Empty(t, snap.RecentlyActive)
}

func TestPhase3TotalsAreNotCappedByFeedLimits(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	r := f.room("busy")
	f.member(viewer.ID, r.ID, true)

	for i := 0; i < 55; i++ {
		m := f.message(r.ID, "urgent please ack", func(m *store.Message) {
			m.RequiresAck = true
			m.Priority = store.PriorityUrgent
			m.CreatedAt = time.Now().Add(time.Duration(i) * time.Second).UTC()
		})
		f.mention(m.ID, viewer.ID)
		f.state(m.ID, viewer.ID, nil, nil)
	}

	snap := f.build(viewer.ID)
	assert.Len(t, snap.Mentions, 50, "feed is intentionally capped")
	assert.Len(t, snap.PendingAcks, 50, "feed is intentionally capped")
	assert.Len(t, snap.Priority, 50, "feed is intentionally capped")
	assert.Equal(t, 55, snap.Totals.Mentions, "totals must count all matching rows, not only the visible feed")
	assert.Equal(t, 55, snap.Totals.PendingAcks, "totals must count all matching rows, not only the visible feed")
	assert.Equal(t, 55, snap.Totals.Priority, "totals must count all matching rows, not only the visible feed")
}

func TestPhase3NewRoomsStaysSubscribedPrimaryOnly(t *testing.T) {
	f := newFx(t)
	viewer := f.account("viewer", "u-viewer")
	rSubbed := f.room("subbed")
	rObserver := f.room("observer")
	now := time.Now().UTC()
	f.joinedAt(viewer.ID, rSubbed.ID, true, now)
	f.joinedAt(viewer.ID, rObserver.ID, false, now)

	snap := f.build(viewer.ID)
	require.Len(t, snap.NewRooms, 1)
	assert.Equal(t, rSubbed.ID, snap.NewRooms[0].RoomID)
}

func TestBuildRejectsEmptyAccount(t *testing.T) {
	f := newFx(t)
	_, err := f.agg.Build(context.Background(), "", 1)
	require.Error(t, err)
}
