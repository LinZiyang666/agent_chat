package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMessageMentionRepoSetAndList exercises the basic write/read path
// of the M9 message_mentions table. The repo is the seam the ingester
// and (later, M9 Phase 2) the send handler use to fan a single
// platform-level @-mention list out into per-account mention rows that
// the state aggregator can join against in CountMentionsForSubscribed
// / ListMentionsForSubscribed.
func TestMessageMentionRepoSetAndList(t *testing.T) {
	s := newM4Store(t)
	viewer := mustCreateAccount(t, s, "viewer")
	other := mustCreateAccount(t, s, "other")
	r := mustCreateRoom(t, s, "r", "ch-r")
	m := mustCreateM5Message(t, s, r.ID, "dm-1", "hi")

	repo := s.Bundle().MessageMentions
	ctx := context.Background()

	// Empty by default.
	got, err := repo.ListForMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.Empty(t, got)

	// Insert two distinct accounts.
	require.NoError(t, repo.SetForMessage(ctx, m.ID, []string{viewer.ID, other.ID}))
	got, err = repo.ListForMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{viewer.ID, other.ID}, got)

	// SetForMessage is a replace, not a merge: re-setting with one
	// account drops the other.
	require.NoError(t, repo.SetForMessage(ctx, m.ID, []string{viewer.ID}))
	got, err = repo.ListForMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{viewer.ID}, got)

	// Empty slice clears existing rows.
	require.NoError(t, repo.SetForMessage(ctx, m.ID, nil))
	got, err = repo.ListForMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Empty messageID is a programmer error and must surface as
// InvalidArgument instead of silently corrupting the table.
func TestMessageMentionRepoRejectsEmptyMessageID(t *testing.T) {
	s := newM4Store(t)
	err := s.Bundle().MessageMentions.SetForMessage(context.Background(), "", []string{"a"})
	require.Error(t, err)
}

// AddForMessage merges rows into the existing mention set rather than
// replacing it. This is the seam the ingester uses on its conflict
// path so a gateway echo never wipes mentions the send handler may
// have written first.
func TestMessageMentionRepoAddForMessageUnions(t *testing.T) {
	s := newM4Store(t)
	a := mustCreateAccount(t, s, "a")
	b := mustCreateAccount(t, s, "b")
	c := mustCreateAccount(t, s, "c")
	r := mustCreateRoom(t, s, "r", "ch-r")
	m := mustCreateM5Message(t, s, r.ID, "dm-1", "hi")

	repo := s.Bundle().MessageMentions
	ctx := context.Background()

	// Seed with two accounts.
	require.NoError(t, repo.SetForMessage(ctx, m.ID, []string{a.ID, b.ID}))

	// Add adds a new account AND re-adds an existing one without
	// erroring (INSERT OR IGNORE on the (msg_id, account_id) PK).
	require.NoError(t, repo.AddForMessage(ctx, m.ID, []string{b.ID, c.ID}))

	got, err := repo.ListForMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a.ID, b.ID, c.ID}, got,
		"Add must union, not replace")

	// Add with an empty slice is a no-op.
	require.NoError(t, repo.AddForMessage(ctx, m.ID, nil))
	got, err = repo.ListForMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a.ID, b.ID, c.ID}, got)
}

// Cascading delete: removing a message must clear its mention rows. The
// FK on message_mentions(message_id) has ON DELETE CASCADE so the
// aggregator never returns stale mentions pointing at a vanished msg.
func TestMessageMentionRepoCascadesOnMessageDelete(t *testing.T) {
	s := newM4Store(t)
	viewer := mustCreateAccount(t, s, "viewer")
	r := mustCreateRoom(t, s, "r", "ch-r")
	m := mustCreateM5Message(t, s, r.ID, "dm-1", "hi")

	require.NoError(t, s.Bundle().MessageMentions.SetForMessage(
		context.Background(), m.ID, []string{viewer.ID}))

	// Cascade fires via the messages FK on rooms; deleting the room
	// (the only API path that fans down to messages) takes the
	// message and its mention with it. The simplest cascade trigger
	// here is to drop the messages row directly via raw SQL.
	_, err := s.DB().ExecContext(context.Background(),
		`DELETE FROM messages WHERE id = ?`, m.ID)
	require.NoError(t, err)

	got, err := s.Bundle().MessageMentions.ListForMessage(context.Background(), m.ID)
	require.NoError(t, err)
	assert.Empty(t, got)
}
