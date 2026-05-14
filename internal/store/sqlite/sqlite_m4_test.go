package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

func newM4Store(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "m4.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return id.String()
}

func mustCreateAccount(t *testing.T, s *Store, name string) *store.Account {
	t.Helper()
	now := time.Now().UTC()
	a := &store.Account{
		ID:             newID(t),
		Name:           name,
		Role:           store.RoleUser,
		LifecycleState: store.LifecycleCreated,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(t, s.Bundle().Accounts.Create(context.Background(), a))
	return a
}

func mustCreateRoom(t *testing.T, s *Store, name, channelID string) *store.Room {
	t.Helper()
	now := time.Now().UTC()
	r := &store.Room{
		ID:               newID(t),
		DiscordChannelID: channelID,
		Name:             name,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, s.Bundle().Rooms.Create(context.Background(), r))
	return r
}

func TestAccountBotUserIDRoundTrip(t *testing.T) {
	s := newM4Store(t)
	a := mustCreateAccount(t, s, "with-bot")
	a.BotUserID = "discord-uid-123"
	a.UpdatedAt = time.Now().UTC()
	require.NoError(t, s.Bundle().Accounts.Update(context.Background(), a))

	loaded, err := s.Bundle().Accounts.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "discord-uid-123", loaded.BotUserID)
}

func TestRoomRepoCRUD(t *testing.T) {
	s := newM4Store(t)
	rooms := s.Bundle().Rooms

	r := mustCreateRoom(t, s, "alpha", "ch-1")

	// Conflict on duplicate discord_channel_id.
	dup := *r
	dup.ID = newID(t)
	dup.Name = "alpha-dup"
	err := rooms.Create(context.Background(), &dup)
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.Conflict, ec.Code)

	got, err := rooms.Get(context.Background(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)
	assert.False(t, got.Archived)

	byChan, err := rooms.GetByDiscordChannelID(context.Background(), "ch-1")
	require.NoError(t, err)
	assert.Equal(t, r.ID, byChan.ID)

	// Update rename + archive.
	r.Name = "alpha-renamed"
	r.Archived = true
	r.UpdatedAt = time.Now().UTC()
	require.NoError(t, rooms.Update(context.Background(), r))
	got, _ = rooms.Get(context.Background(), r.ID)
	assert.Equal(t, "alpha-renamed", got.Name)
	assert.True(t, got.Archived)

	// List excludes archived by default.
	all, err := rooms.List(context.Background(), false)
	require.NoError(t, err)
	assert.Len(t, all, 0)
	all, err = rooms.List(context.Background(), true)
	require.NoError(t, err)
	assert.Len(t, all, 1)

	require.NoError(t, rooms.Delete(context.Background(), r.ID))
	_, err = rooms.Get(context.Background(), r.ID)
	require.Error(t, err)
	ec, _ = errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.NotFound, ec.Code)
}

func TestMembershipUpsertPreservesJoinedAt(t *testing.T) {
	s := newM4Store(t)
	a := mustCreateAccount(t, s, "u")
	r := mustCreateRoom(t, s, "r", "ch")
	bundle := s.Bundle()
	joined := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, bundle.Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: a.ID, RoomID: r.ID, Subscribed: false, JoinedAt: joined,
	}))

	// Toggle subscribed; joined_at must not change.
	later := joined.Add(time.Hour)
	require.NoError(t, bundle.Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: a.ID, RoomID: r.ID, Subscribed: true, JoinedAt: later,
	}))
	got, err := bundle.Memberships.Get(context.Background(), a.ID, r.ID)
	require.NoError(t, err)
	assert.True(t, got.Subscribed)
	assert.True(t, got.JoinedAt.Equal(joined),
		"joined_at must not move on subscribe-toggle (got %v want %v)", got.JoinedAt, joined)
}

func TestMembershipListSubscribersFilters(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	a1 := mustCreateAccount(t, s, "sub1")
	a2 := mustCreateAccount(t, s, "lurk1")
	bundle := s.Bundle()
	now := time.Now().UTC()
	require.NoError(t, bundle.Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: a1.ID, RoomID: r.ID, Subscribed: true, JoinedAt: now,
	}))
	require.NoError(t, bundle.Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: a2.ID, RoomID: r.ID, Subscribed: false, JoinedAt: now,
	}))

	subs, err := bundle.Memberships.ListSubscribers(context.Background(), r.ID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, a1.ID, subs[0].AccountID)
}

func TestMessageCreateAndDedupe(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	bundle := s.Bundle()
	msg := &store.Message{
		ID:           newID(t),
		RoomID:       r.ID,
		DiscordMsgID: "dms-1",
		Content:      "hello",
		Priority:     store.PriorityNormal,
		CreatedAt:    time.Now().UTC(),
		ContentHash:  "h1",
	}
	require.NoError(t, bundle.Messages.Create(context.Background(), msg))

	// Duplicate discord_msg_id: Conflict via Create.
	dup := *msg
	dup.ID = newID(t)
	err := bundle.Messages.Create(context.Background(), &dup)
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.Conflict, ec.Code)

	// Same duplicate via CreateIgnoreConflict: returns the existing
	// row's id and inserted=false.
	dup2 := *msg
	dup2.ID = newID(t)
	id, inserted, err := bundle.Messages.CreateIgnoreConflict(context.Background(), &dup2)
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Equal(t, msg.ID, id)
}

func TestMessageListOrderingAndPaging(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	bundle := s.Bundle()
	base := time.Unix(1700000000, 0).UTC()
	var ids []string
	for i := 0; i < 5; i++ {
		m := &store.Message{
			ID:           newID(t),
			RoomID:       r.ID,
			DiscordMsgID: "dm-" + uuid.NewString(),
			Content:      "x",
			Priority:     store.PriorityNormal,
			CreatedAt:    base.Add(time.Duration(i) * time.Second),
			ContentHash:  "h",
		}
		require.NoError(t, bundle.Messages.Create(context.Background(), m))
		ids = append(ids, m.ID)
	}
	// List with no before: newest-first.
	msgs, err := bundle.Messages.List(context.Background(), store.MessageFilter{RoomID: r.ID})
	require.NoError(t, err)
	require.Len(t, msgs, 5)
	assert.Equal(t, ids[4], msgs[0].ID, "newest first")
	assert.Equal(t, ids[0], msgs[4].ID, "oldest last")

	// Page: before = ids[2] should return ids[1], ids[0] only.
	page, err := bundle.Messages.List(context.Background(), store.MessageFilter{
		RoomID: r.ID, BeforeID: ids[2], Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, ids[1], page[0].ID)
	assert.Equal(t, ids[0], page[1].ID)
}

func TestMessageStateUpsertPreservesPriorTimestamp(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	a := mustCreateAccount(t, s, "u")
	bundle := s.Bundle()
	msg := &store.Message{
		ID: newID(t), RoomID: r.ID, DiscordMsgID: "x",
		Content: "c", Priority: store.PriorityNormal,
		CreatedAt: time.Now().UTC(), ContentHash: "h",
	}
	require.NoError(t, bundle.Messages.Create(context.Background(), msg))

	// First mark read.
	first := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, bundle.MessageStates.Upsert(context.Background(), &store.MessageState{
		MessageID: msg.ID, AccountID: a.ID, ReadAt: &first,
	}))

	// Second upsert with no read_at must NOT clobber the existing read_at.
	require.NoError(t, bundle.MessageStates.Upsert(context.Background(), &store.MessageState{
		MessageID: msg.ID, AccountID: a.ID,
	}))
	got, err := bundle.MessageStates.Get(context.Background(), msg.ID, a.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ReadAt)
	assert.True(t, got.ReadAt.Equal(first),
		"existing read_at must be preserved (got %v want %v)", got.ReadAt, first)
}

func TestBundlerWithTxRollsBackOnError(t *testing.T) {
	s := newM4Store(t)
	a := mustCreateAccount(t, s, "u")
	r := mustCreateRoom(t, s, "r", "ch")
	now := time.Now().UTC()

	// Inside a tx: insert a membership then return an error. The
	// membership row must NOT survive.
	err := s.WithTx(context.Background(), func(b store.Bundle) error {
		if err := b.Memberships.Upsert(context.Background(), &store.Membership{
			AccountID: a.ID, RoomID: r.ID, Subscribed: true, JoinedAt: now,
		}); err != nil {
			return err
		}
		return assertFakeFailure
	})
	require.ErrorIs(t, err, assertFakeFailure)

	_, err = s.Bundle().Memberships.Get(context.Background(), a.ID, r.ID)
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.NotFound, ec.Code)
}

var assertFakeFailure = errcode.New(errcode.Internal, "fake failure")

func TestRoomListByMember(t *testing.T) {
	s := newM4Store(t)
	a := mustCreateAccount(t, s, "u")
	r1 := mustCreateRoom(t, s, "joined", "ch-1")
	_ = mustCreateRoom(t, s, "not-joined", "ch-2")
	require.NoError(t, s.Bundle().Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: a.ID, RoomID: r1.ID, Subscribed: false, JoinedAt: time.Now().UTC(),
	}))

	rooms, err := s.Bundle().Rooms.ListByMember(context.Background(), a.ID, false)
	require.NoError(t, err)
	require.Len(t, rooms, 1)
	assert.Equal(t, r1.ID, rooms[0].ID)
}

func TestMembershipListsAndSetSubscribed(t *testing.T) {
	s := newM4Store(t)
	a := mustCreateAccount(t, s, "u")
	r := mustCreateRoom(t, s, "r", "ch")
	now := time.Now().UTC()
	require.NoError(t, s.Bundle().Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: a.ID, RoomID: r.ID, Subscribed: false, JoinedAt: now,
	}))

	byRoom, err := s.Bundle().Memberships.ListByRoom(context.Background(), r.ID)
	require.NoError(t, err)
	require.Len(t, byRoom, 1)

	byAccount, err := s.Bundle().Memberships.ListByAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.Len(t, byAccount, 1)

	require.NoError(t, s.Bundle().Memberships.SetSubscribed(context.Background(), a.ID, r.ID, true))
	got, err := s.Bundle().Memberships.Get(context.Background(), a.ID, r.ID)
	require.NoError(t, err)
	assert.True(t, got.Subscribed)

	require.NoError(t, s.Bundle().Memberships.Delete(context.Background(), a.ID, r.ID))
	_, err = s.Bundle().Memberships.Get(context.Background(), a.ID, r.ID)
	require.Error(t, err)
}

func TestMessageGetterPaths(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	m := &store.Message{
		ID: newID(t), RoomID: r.ID, DiscordMsgID: "dm-look-me-up",
		Content: "c", Priority: store.PriorityNormal,
		CreatedAt: time.Now().UTC(), ContentHash: "h",
	}
	require.NoError(t, s.Bundle().Messages.Create(context.Background(), m))

	gotByID, err := s.Bundle().Messages.Get(context.Background(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, "c", gotByID.Content)

	gotByDiscord, err := s.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-look-me-up")
	require.NoError(t, err)
	assert.Equal(t, m.ID, gotByDiscord.ID)

	_, err = s.Bundle().Messages.Get(context.Background(), "missing")
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.NotFound, ec.Code)
}

func TestMessageApplySendMetadata(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	author := mustCreateAccount(t, s, "author")

	// Stand in for the ingester-created row: minimal metadata,
	// author_account_id NULL.
	preexisting := &store.Message{
		ID:           newID(t),
		RoomID:       r.ID,
		DiscordMsgID: "dm-race",
		Content:      "x",
		Priority:     store.PriorityNormal,
		CreatedAt:    time.Now().UTC(),
		ContentHash:  "ingester-hash",
	}
	require.NoError(t, s.Bundle().Messages.Create(context.Background(), preexisting))

	// Apply send-path metadata.
	require.NoError(t, s.Bundle().Messages.ApplySendMetadata(context.Background(), preexisting.ID, store.SendMetadata{
		AuthorAccountID: author.ID,
		ReplyToMsgID:    "",
		RequiresAck:     true,
		Priority:        store.PriorityUrgent,
		ContentHash:     "sender-hash",
	}))

	got, err := s.Bundle().Messages.Get(context.Background(), preexisting.ID)
	require.NoError(t, err)
	assert.Equal(t, author.ID, got.AuthorAccountID)
	assert.True(t, got.RequiresAck)
	assert.Equal(t, store.PriorityUrgent, got.Priority)
	assert.Equal(t, "sender-hash", got.ContentHash)

	// Apply with invalid priority rejects.
	err = s.Bundle().Messages.ApplySendMetadata(context.Background(), preexisting.ID, store.SendMetadata{
		Priority: store.MessagePriority("bogus"),
	})
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.InvalidArgument, ec.Code)

	// Apply on a missing id surfaces NotFound.
	err = s.Bundle().Messages.ApplySendMetadata(context.Background(), "nonexistent", store.SendMetadata{
		Priority: store.PriorityNormal,
	})
	require.Error(t, err)
	ec, _ = errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.NotFound, ec.Code)
}

func TestMessageStateListByAccount(t *testing.T) {
	s := newM4Store(t)
	r := mustCreateRoom(t, s, "r", "ch")
	a := mustCreateAccount(t, s, "u")
	m := &store.Message{
		ID: newID(t), RoomID: r.ID, DiscordMsgID: "dm-sx",
		Content: "c", Priority: store.PriorityNormal,
		CreatedAt: time.Now().UTC(), ContentHash: "h",
	}
	require.NoError(t, s.Bundle().Messages.Create(context.Background(), m))
	now := time.Now().UTC()
	require.NoError(t, s.Bundle().MessageStates.Upsert(context.Background(), &store.MessageState{
		MessageID: m.ID, AccountID: a.ID, ReadAt: &now,
	}))

	states, err := s.Bundle().MessageStates.ListByAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, m.ID, states[0].MessageID)
	require.NotNil(t, states[0].ReadAt)
}
