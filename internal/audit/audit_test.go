package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

func newRec(t *testing.T) (*Recorder, store.AuditRepo) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	rep := s.Bundle().Audit
	r := NewRecorder(rep)
	return r, rep
}

func TestRecordRoundTrip(t *testing.T) {
	r, repo := newRec(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return now })

	err := r.Record(ctx, "root", ActionAccountCreate, "acc-1", map[string]any{"name": "alice"})
	require.NoError(t, err)

	got, err := repo.List(ctx, store.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "root", got[0].AccountID)
	assert.Equal(t, string(ActionAccountCreate), got[0].Action)
	assert.Equal(t, "acc-1", got[0].Target)
	assert.Contains(t, got[0].Payload, `"name":"alice"`)
	assert.WithinDuration(t, now, got[0].CreatedAt, time.Second)
}

func TestRecordNilPayload(t *testing.T) {
	r, repo := newRec(t)
	require.NoError(t, r.Record(context.Background(), "root", ActionTokenRevoke, "tok", nil))
	got, _ := repo.List(context.Background(), store.AuditFilter{})
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Payload)
}

func TestRecordRejectsEmptyAction(t *testing.T) {
	r, _ := newRec(t)
	err := r.Record(context.Background(), "root", "", "x", nil)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestListPropagates(t *testing.T) {
	r, _ := newRec(t)
	require.NoError(t, r.Record(context.Background(), "root", ActionAccountSetRole, "acc-1", nil))
	got, err := r.List(context.Background(), store.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
}
