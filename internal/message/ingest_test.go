package message

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// fixture wraps a real sqlite Store + an Ingester (no Connector
// goroutines — we call ingestNew directly to keep the test
// deterministic).
type fixture struct {
	t     *testing.T
	store *sqlite.Store
	ing   *Ingester
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "ing.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	conn := connector.New(func(_ string, _ bot.Identity) bot.Provider {
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &fixture{
		t:     t,
		store: s,
		ing:   New(conn, s, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

func (f *fixture) createAccount(name string, botUserID string) *store.Account {
	now := time.Now().UTC()
	id, err := uuid.NewV7()
	require.NoError(f.t, err)
	a := &store.Account{
		ID:             id.String(),
		Name:           name,
		Role:           store.RoleUser,
		LifecycleState: store.LifecycleOnline,
		BotUserID:      botUserID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(f.t, f.store.Bundle().Accounts.Create(context.Background(), a))
	return a
}

func (f *fixture) createRoom(name, channelID string) *store.Room {
	now := time.Now().UTC()
	id, err := uuid.NewV7()
	require.NoError(f.t, err)
	r := &store.Room{
		ID:               id.String(),
		DiscordChannelID: channelID,
		Name:             name,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(f.t, f.store.Bundle().Rooms.Create(context.Background(), r))
	return r
}

func (f *fixture) addMember(acc, room string, subscribed bool) {
	require.NoError(f.t, f.store.Bundle().Memberships.Upsert(context.Background(), &store.Membership{
		AccountID: acc, RoomID: room, Subscribed: subscribed, JoinedAt: time.Now().UTC(),
	}))
}

func (f *fixture) ingest(ev bot.EventMessageNew) error {
	return f.ing.ingestNew("ingester-acc", ev)
}

func TestIngestUnknownChannelIsDropped(t *testing.T) {
	f := newFixture(t)
	err := f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID: "outside-1", ChannelID: "ch-unmapped", AuthorID: "u-x",
		Content: "dropped", CreatedAt: time.Now(),
	}})
	require.NoError(t, err, "unmapped channel must NOT error; just drop")
	// Verify nothing landed in messages.
	got, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "outside-1")
	require.Error(t, err, "no message should be persisted")
	assert.Nil(t, got)
}

func TestIngestFanOutCoversAllMembers(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-1")
	subbed := f.createAccount("sub", "u-sub")
	unsubbed := f.createAccount("lurker", "u-lurker")
	f.addMember(subbed.ID, room.ID, true)
	f.addMember(unsubbed.ID, room.ID, false)

	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID: "dm-1", ChannelID: "ch-1", AuthorID: "u-external",
		Content: "external", CreatedAt: time.Now(),
	}}))

	persisted, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-1")
	require.NoError(t, err)

	// Both subscribed and unsubscribed members got a state row.
	for _, acc := range []string{subbed.ID, unsubbed.ID} {
		st, err := f.store.Bundle().MessageStates.Get(context.Background(), persisted.ID, acc)
		require.NoError(t, err, "state row missing for %s", acc)
		assert.Nil(t, st.ReadAt, "non-author state must be unread")
	}
}

func TestIngestAuthorReadStateForOurBot(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-2")
	ours := f.createAccount("ours", "u-bot-1")
	f.addMember(ours.ID, room.ID, true)

	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID: "dm-2", ChannelID: "ch-2", AuthorID: "u-bot-1",
		Content: "self", CreatedAt: time.Now(),
	}}))
	persisted, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-2")
	require.NoError(t, err)
	assert.Equal(t, ours.ID, persisted.AuthorAccountID,
		"author resolved to the agentchat account whose bot_user_id matches")

	st, err := f.store.Bundle().MessageStates.Get(context.Background(), persisted.ID, ours.ID)
	require.NoError(t, err)
	require.NotNil(t, st.ReadAt, "author state must be read")
}

func TestIngestDedupesOnDiscordMsgID(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-3")
	member := f.createAccount("m", "u-m")
	f.addMember(member.ID, room.ID, true)

	// First ingest creates the row.
	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID: "dm-3", ChannelID: "ch-3", AuthorID: "u-external",
		Content: "v1", CreatedAt: time.Now(),
	}}))
	first, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-3")
	require.NoError(t, err)

	// Second ingest with same discord_msg_id but different content
	// must NOT replace the row.
	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID: "dm-3", ChannelID: "ch-3", AuthorID: "u-external",
		Content: "v2", CreatedAt: time.Now(),
	}}))
	second, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-3")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "no new id")
	assert.Equal(t, "v1", second.Content, "original content preserved")
}

func TestIngestExternalAuthorLeftEmpty(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-4")
	member := f.createAccount("m", "u-known")
	f.addMember(member.ID, room.ID, false)

	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID: "dm-4", ChannelID: "ch-4", AuthorID: "u-unmatched-discord-user",
		Content: "from-stranger", CreatedAt: time.Now(),
	}}))
	got, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-4")
	require.NoError(t, err)
	assert.Empty(t, got.AuthorAccountID,
		"unmatched discord author_id must leave author_account_id NULL")

	// The member's state row still exists, unread.
	st, err := f.store.Bundle().MessageStates.Get(context.Background(), got.ID, member.ID)
	require.NoError(t, err)
	assert.Nil(t, st.ReadAt)
}

func TestResolveAuthorEmptyDiscordUserID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bundle := f.store.Bundle()
	got, err := f.ing.resolveAuthor(ctx, bundle, "")
	require.NoError(t, err)
	assert.Empty(t, got)
}
