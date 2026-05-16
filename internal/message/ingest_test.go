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
		ing:   New(conn, s, slog.New(slog.NewTextHandler(io.Discard, nil)), nil),
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

func TestIngestPersistsDiscordMentions(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-mentions")
	mentioned := f.createAccount("mentioned", "u-mentioned")
	other := f.createAccount("other", "u-other")
	f.addMember(mentioned.ID, room.ID, true)
	f.addMember(other.ID, room.ID, true)

	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID:                  "dm-mentions",
		ChannelID:           "ch-mentions",
		AuthorID:            "u-external",
		Content:             "please look",
		CreatedAt:           time.Now(),
		MentionedBotUserIDs: []string{"u-mentioned", "u-mentioned", "u-unknown"},
		MentionEveryone:     true,
	}}))

	got, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-mentions")
	require.NoError(t, err)
	assert.True(t, got.MentionEveryone)

	mentions, err := f.store.Bundle().MessageMentions.ListForMessage(context.Background(), got.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{mentioned.ID}, mentions)
}

// M9 Phase 1 review P2-2: Discord MESSAGE_CREATE.mentions can name
// accounts that are NOT current room members. The ingester must drop
// those before writing message_mentions, otherwise non-members get
// false mention rows that the state aggregator's joins can't always
// shield (and that future read <room> endpoints would surface).
func TestIngestDropsMentionsForNonMembers(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-non-member")
	member := f.createAccount("inroom", "u-inroom")
	stranger := f.createAccount("outroom", "u-outroom")
	f.addMember(member.ID, room.ID, true)
	// stranger has an agentchat account + bot_user_id but is NOT
	// in this room.

	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID:                  "dm-non-member",
		ChannelID:           "ch-non-member",
		AuthorID:            "u-external",
		Content:             "ping both",
		CreatedAt:           time.Now(),
		MentionedBotUserIDs: []string{"u-inroom", "u-outroom"},
	}}))

	got, err := f.store.Bundle().Messages.GetByDiscordMsgID(context.Background(), "dm-non-member")
	require.NoError(t, err)

	mentions, err := f.store.Bundle().MessageMentions.ListForMessage(context.Background(), got.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{member.ID}, mentions,
		"stranger %s is not a room member; message_mentions must NOT include them",
		stranger.ID)
}

// M9 Phase 1 review P2-1: when the send handler races ahead and writes
// the messages row first, the gateway echo arriving later still
// carries the authoritative Discord-side mention list. The ingester's
// conflict path must merge that metadata in (mention_everyone OR-merge
// + message_mentions union) rather than dropping it.
func TestIngestConflictPathMergesMentionMetadata(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-conflict")
	mentioned := f.createAccount("mentioned", "u-mentioned")
	f.addMember(mentioned.ID, room.ID, true)

	ctx := context.Background()

	// Simulate the send-path write: a messages row with the same
	// discord_msg_id, but no mention metadata yet (the M9 Phase 1
	// send handler still mirrors --all to mention_everyone, but does
	// NOT yet parse outbound `@name` — that's Phase 2). We hand-write
	// the row directly to keep the test focused on the ingester.
	existing := &store.Message{
		ID:           "send-existing-id",
		RoomID:       room.ID,
		DiscordMsgID: "dm-conflict",
		Content:      "send-side wrote first",
		Priority:     store.PriorityNormal,
		CreatedAt:    time.Now().UTC(),
		ContentHash:  "h-conflict",
	}
	require.NoError(t, f.store.Bundle().Messages.Create(ctx, existing))

	// Now the gateway echo arrives carrying real Discord mention
	// metadata.
	require.NoError(t, f.ingest(bot.EventMessageNew{Message: bot.Message{
		ID:                  "dm-conflict",
		ChannelID:           "ch-conflict",
		AuthorID:            "u-external",
		Content:             "echo with mentions",
		CreatedAt:           time.Now(),
		MentionedBotUserIDs: []string{"u-mentioned"},
		MentionEveryone:     true,
	}}))

	merged, err := f.store.Bundle().Messages.GetByDiscordMsgID(ctx, "dm-conflict")
	require.NoError(t, err)
	assert.Equal(t, existing.ID, merged.ID, "conflict path must NOT replace the row")
	assert.True(t, merged.MentionEveryone, "mention_everyone must be OR-merged in")

	mentions, err := f.store.Bundle().MessageMentions.ListForMessage(ctx, merged.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{mentioned.ID}, mentions,
		"per-user mention from echo must land in message_mentions")
}

// M9 Phase 1 review P2-1: ApplySendMetadata uses MAX(existing, new) on
// mention_everyone so a later send-side write of false cannot clobber a
// true that the ingester already observed from the gateway echo.
func TestApplySendMetadataDoesNotClobberMentionEveryone(t *testing.T) {
	f := newFixture(t)
	room := f.createRoom("r", "ch-clobber")
	ctx := context.Background()

	// Pretend the ingester wrote the row first with mention_everyone=true.
	row := &store.Message{
		ID:              "ing-id",
		RoomID:          room.ID,
		DiscordMsgID:    "dm-clobber",
		Content:         "from echo",
		Priority:        store.PriorityNormal,
		CreatedAt:       time.Now().UTC(),
		ContentHash:     "h-clobber",
		MentionEveryone: true,
	}
	require.NoError(t, f.store.Bundle().Messages.Create(ctx, row))

	// Now the send handler races behind and calls ApplySendMetadata
	// with MentionEveryone=false. The OR-merge must keep the existing
	// true.
	require.NoError(t, f.store.Bundle().Messages.ApplySendMetadata(ctx, row.ID, store.SendMetadata{
		AuthorAccountID: "",
		ReplyToMsgID:    "",
		Priority:        store.PriorityNormal,
		ContentHash:     "h-clobber",
		MentionEveryone: false, // explicit false from send
	}))

	after, err := f.store.Bundle().Messages.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.True(t, after.MentionEveryone,
		"mention_everyone must stick after a subsequent send-side write of false")
}

func TestResolveAuthorEmptyDiscordUserID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bundle := f.store.Bundle()
	got, err := f.ing.resolveAuthor(ctx, bundle, "")
	require.NoError(t, err)
	assert.Empty(t, got)
}
