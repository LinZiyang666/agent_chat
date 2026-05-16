package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/store"
)

func mustCreateM5Message(t *testing.T, s *Store, roomID, discordID, content string, opts ...func(*store.Message)) *store.Message {
	t.Helper()
	m := &store.Message{
		ID:           newID(t),
		RoomID:       roomID,
		DiscordMsgID: discordID,
		Content:      content,
		Priority:     store.PriorityNormal,
		CreatedAt:    time.Now().UTC(),
		ContentHash:  "h-" + discordID,
	}
	for _, opt := range opts {
		opt(m)
	}
	require.NoError(t, s.Bundle().Messages.Create(context.Background(), m))
	return m
}

// mustCreateM5State seeds a per-account read state row. The M6-era
// replied_at slot was removed in M9 Phase 2; the parameter survives
// only to avoid churning every call site.
func mustCreateM5State(t *testing.T, s *Store, messageID, accountID string, readAt, _ *time.Time) {
	t.Helper()
	require.NoError(t, s.Bundle().MessageStates.Upsert(context.Background(), &store.MessageState{
		MessageID: messageID,
		AccountID: accountID,
		ReadAt:    readAt,
	}))
}

func TestM5MessageStateReadPathsScopeToSubscribedNonArchivedRooms(t *testing.T) {
	s := newM4Store(t)
	viewer := mustCreateAccount(t, s, "viewer")
	active := mustCreateRoom(t, s, "active", "ch-active")
	observer := mustCreateRoom(t, s, "observer", "ch-observer")
	archived := mustCreateRoom(t, s, "archived", "ch-archived")
	archived.Archived = true
	archived.UpdatedAt = time.Now().UTC()
	require.NoError(t, s.Bundle().Rooms.Update(context.Background(), archived))

	now := time.Now().UTC()
	for _, m := range []*store.Membership{
		{AccountID: viewer.ID, RoomID: active.ID, Subscribed: true, JoinedAt: now},
		{AccountID: viewer.ID, RoomID: observer.ID, Subscribed: false, JoinedAt: now},
		{AccountID: viewer.ID, RoomID: archived.ID, Subscribed: true, JoinedAt: now},
	} {
		require.NoError(t, s.Bundle().Memberships.Upsert(context.Background(), m))
	}

	// M9 Phase 1: mention semantics moved off content-LIKE onto
	// message_mentions. The msg in each room gets a mention row for
	// the viewer; expected pre/post-filter counts are identical to
	// the M6 baseline but the source of truth is the new table.
	makeMsg := func(roomID, discordID string) *store.Message {
		m := mustCreateM5Message(t, s, roomID, discordID, "urgent please ack", func(m *store.Message) {
			m.Priority = store.PriorityUrgent
		})
		mustCreateM5State(t, s, m.ID, viewer.ID, nil, nil)
		require.NoError(t, s.Bundle().MessageMentions.SetForMessage(
			context.Background(), m.ID, []string{viewer.ID}))
		return m
	}
	activeMsg := makeMsg(active.ID, "dm-active")
	_ = makeMsg(observer.ID, "dm-observer")
	_ = makeMsg(archived.ID, "dm-archived")

	repo := s.Bundle().MessageStates
	unread, err := repo.CountUnreadForSubscribed(context.Background(), viewer.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, unread)
	mentions, err := repo.CountMentionsForSubscribed(context.Background(), viewer.ID, "bot-viewer")
	require.NoError(t, err)
	assert.Equal(t, 1, mentions)
	priority, err := repo.CountPriorityForSubscribed(context.Background(), viewer.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, priority)

	byRoom, err := repo.UnreadCountByRoomForSubscribed(context.Background(), viewer.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{active.ID: 1}, byRoom)

	mentionRows, err := repo.ListMentionsForSubscribed(context.Background(), viewer.ID, "bot-viewer", 50)
	require.NoError(t, err)
	require.Len(t, mentionRows, 1)
	assert.Equal(t, activeMsg.ID, mentionRows[0].ID)
	priorityRows, err := repo.ListPriorityForSubscribed(context.Background(), viewer.ID, 50)
	require.NoError(t, err)
	require.Len(t, priorityRows, 1)
	assert.Equal(t, activeMsg.ID, priorityRows[0].ID)
}

func TestM5MembershipJoinAndLatestMessageReadModels(t *testing.T) {
	s := newM4Store(t)
	viewer := mustCreateAccount(t, s, "viewer")
	active := mustCreateRoom(t, s, "active", "ch-active")
	archived := mustCreateRoom(t, s, "archived", "ch-archived")
	archived.Archived = true
	archived.UpdatedAt = time.Now().UTC()
	require.NoError(t, s.Bundle().Rooms.Update(context.Background(), archived))

	joinedAt := time.Now().UTC()
	require.NoError(t, s.Bundle().Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: viewer.ID, RoomID: active.ID, Subscribed: true, JoinedAt: joinedAt,
	}))
	require.NoError(t, s.Bundle().Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: viewer.ID, RoomID: archived.ID, Subscribed: true, JoinedAt: joinedAt.Add(time.Second),
	}))

	activeMsg := mustCreateM5Message(t, s, active.ID, "dm-active", "active")
	_ = mustCreateM5Message(t, s, archived.ID, "dm-archived", "archived")

	joined, err := s.Bundle().Memberships.ListByAccountWithRooms(context.Background(), viewer.ID)
	require.NoError(t, err)
	require.Len(t, joined, 2)
	assert.Equal(t, active.ID, joined[0].Room.ID)
	assert.Equal(t, active.ID, joined[0].Membership.RoomID)
	assert.Equal(t, archived.ID, joined[1].Room.ID)
	assert.True(t, joined[1].Room.Archived)

	latest, err := s.Bundle().Messages.LatestPerRoomForMember(context.Background(), viewer.ID)
	require.NoError(t, err)
	require.Len(t, latest, 1)
	require.Contains(t, latest, active.ID)
	assert.Equal(t, activeMsg.ID, latest[active.ID].ID)
}
