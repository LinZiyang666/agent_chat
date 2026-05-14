package account

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return NewService(s.Bundle().Accounts)
}

func TestCreateHappy(t *testing.T) {
	svc := newSvc(t)
	a, err := svc.Create(context.Background(), "alice", store.RoleAdmin)
	require.NoError(t, err)
	require.NotNil(t, a)
	assert.NotEmpty(t, a.ID)
	assert.Equal(t, "alice", a.Name)
	assert.Equal(t, store.RoleAdmin, a.Role)
	assert.Equal(t, store.LifecycleCreated, a.LifecycleState)
}

func TestCreateRejectsInvalid(t *testing.T) {
	svc := newSvc(t)
	cases := []struct {
		name string
		nm   string
		role store.Role
		want errcode.Code
	}{
		{"empty name", "", store.RoleUser, errcode.InvalidArgument},
		{"whitespace", " bob ", store.RoleUser, errcode.InvalidArgument},
		{"too long", strings.Repeat("a", 65), store.RoleUser, errcode.InvalidArgument},
		{"bad role", "ok", store.Role("wizard"), errcode.InvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), c.nm, c.role)
			require.Error(t, err)
			e, _ := errcode.As(err)
			require.NotNil(t, e)
			assert.Equal(t, c.want, e.Code)
		})
	}
}

func TestCreateDuplicate(t *testing.T) {
	svc := newSvc(t)
	_, err := svc.Create(context.Background(), "dup", store.RoleUser)
	require.NoError(t, err)
	_, err = svc.Create(context.Background(), "dup", store.RoleUser)
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestSetRole(t *testing.T) {
	svc := newSvc(t)
	a, err := svc.Create(context.Background(), "bob", store.RoleUser)
	require.NoError(t, err)
	updated, err := svc.SetRole(context.Background(), a.ID, store.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, updated.Role)
}

func TestSetRoleNotFound(t *testing.T) {
	svc := newSvc(t)
	_, err := svc.SetRole(context.Background(), "ghost", store.RoleAdmin)
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.NotFound, e.Code)
}

func TestSetRoleRejectsInvalid(t *testing.T) {
	svc := newSvc(t)
	a, _ := svc.Create(context.Background(), "x", store.RoleUser)
	_, err := svc.SetRole(context.Background(), a.ID, "wizard")
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestRename(t *testing.T) {
	svc := newSvc(t)
	a, _ := svc.Create(context.Background(), "x", store.RoleUser)
	renamed, err := svc.Rename(context.Background(), a.ID, "y")
	require.NoError(t, err)
	assert.Equal(t, "y", renamed.Name)
}

func TestDelete(t *testing.T) {
	svc := newSvc(t)
	a, _ := svc.Create(context.Background(), "x", store.RoleUser)
	require.NoError(t, svc.Delete(context.Background(), a.ID))
	_, err := svc.Get(context.Background(), a.ID)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.NotFound, e.Code)
}

func TestListOrdered(t *testing.T) {
	svc := newSvc(t)
	tick := time.Now().UTC()
	svc.SetClock(func() time.Time { tick = tick.Add(time.Second); return tick })
	for _, n := range []string{"a", "b", "c"} {
		_, err := svc.Create(context.Background(), n, store.RoleUser)
		require.NoError(t, err)
	}
	got, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "a", got[0].Name)
	assert.Equal(t, "c", got[2].Name)
}

func TestBootstrapRootCreatesOnEmpty(t *testing.T) {
	svc := newSvc(t)
	a, created, err := svc.BootstrapRoot(context.Background())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "root", a.Name)
	assert.Equal(t, store.RoleAdmin, a.Role)
}

func TestBootstrapRootNoopOnPopulated(t *testing.T) {
	svc := newSvc(t)
	_, err := svc.Create(context.Background(), "someone", store.RoleUser)
	require.NoError(t, err)
	a, created, err := svc.BootstrapRoot(context.Background())
	require.NoError(t, err)
	assert.False(t, created)
	assert.Nil(t, a)
}

// TestUpdateAtomicityRejectsBeforeWriting locks in the fix for
// M2-P3-002: a PATCH that supplies a valid `name` and an invalid
// `role` must not rename the account before failing.
func TestUpdateAtomicityRejectsBeforeWriting(t *testing.T) {
	svc := newSvc(t)
	a, err := svc.Create(context.Background(), "atomic-old", store.RoleUser)
	require.NoError(t, err)

	newName := "atomic-new"
	bogusRole := store.Role("wizard")
	_, changes, err := svc.Update(context.Background(), a.ID, UpdateRequest{
		Name: &newName,
		Role: &bogusRole,
	})
	require.Error(t, err)
	assert.Nil(t, changes)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.InvalidArgument, e.Code)

	got, err := svc.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "atomic-old", got.Name, "rename must not have happened on validation failure")
}

func TestUpdateAppliesAllChangesAtOnce(t *testing.T) {
	svc := newSvc(t)
	a, err := svc.Create(context.Background(), "old-name", store.RoleUser)
	require.NoError(t, err)
	newName := "new-name"
	newRole := store.RoleAdmin
	updated, changes, err := svc.Update(context.Background(), a.ID, UpdateRequest{
		Name: &newName,
		Role: &newRole,
	})
	require.NoError(t, err)
	assert.Len(t, changes, 2)
	assert.Equal(t, "new-name", updated.Name)
	assert.Equal(t, store.RoleAdmin, updated.Role)
}

func TestUpdateNoFieldsRejected(t *testing.T) {
	svc := newSvc(t)
	a, _ := svc.Create(context.Background(), "x", store.RoleUser)
	_, _, err := svc.Update(context.Background(), a.ID, UpdateRequest{})
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestUpdateNoopReturnsEmptyChanges(t *testing.T) {
	svc := newSvc(t)
	a, _ := svc.Create(context.Background(), "same", store.RoleUser)
	newName := "same" // identical to current
	newRole := store.RoleUser
	got, changes, err := svc.Update(context.Background(), a.ID, UpdateRequest{
		Name: &newName, Role: &newRole,
	})
	require.NoError(t, err)
	assert.Empty(t, changes)
	assert.Equal(t, "same", got.Name)
}
