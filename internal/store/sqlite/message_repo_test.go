package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// TestMergeMentionEveryoneOrSemantics verifies the M9 Phase 1 merge
// helper: true sticks, false is a no-op. This is what keeps the
// ingester's conflict path (gateway echo) and the send handler's
// ApplySendMetadata from clobbering each other on mention_everyone.
func TestMergeMentionEveryoneOrSemantics(t *testing.T) {
	s := newM4Store(t)
	ctx := context.Background()
	r := mustCreateRoom(t, s, "r", "ch-r")
	m := mustCreateM5Message(t, s, r.ID, "dm-1", "hi")
	repo := s.Bundle().Messages

	// Initially false.
	got, err := repo.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.False(t, got.MentionEveryone)

	// Merge true → becomes true.
	require.NoError(t, repo.MergeMentionEveryone(ctx, m.ID, true))
	got, err = repo.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.True(t, got.MentionEveryone)

	// Merge false on an already-true row → stays true (no-op).
	require.NoError(t, repo.MergeMentionEveryone(ctx, m.ID, false))
	got, err = repo.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.True(t, got.MentionEveryone, "false merge must NOT downgrade an existing true")

	// Merge true again — idempotent.
	require.NoError(t, repo.MergeMentionEveryone(ctx, m.ID, true))
	got, err = repo.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.True(t, got.MentionEveryone)
}

func TestMergeMentionEveryoneNotFoundOnMissingID(t *testing.T) {
	s := newM4Store(t)
	ctx := context.Background()
	err := s.Bundle().Messages.MergeMentionEveryone(ctx, "no-such-id", true)
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.NotFound, ec.Code)

	// Same NotFound contract on the no-op branch (flag=false).
	err = s.Bundle().Messages.MergeMentionEveryone(ctx, "no-such-id", false)
	require.Error(t, err)
	ec, _ = errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.NotFound, ec.Code)
}
