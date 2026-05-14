package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// newTestStore opens a fresh DB inside the test's temp dir. Using a
// real file (not :memory:) exercises the same code path as production
// — including WAL setup — while staying hermetic.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := newTestStore(t)
	row := s.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`)
	var n int
	require.NoError(t, row.Scan(&n))
	assert.GreaterOrEqual(t, n, 1)
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	s1, err := Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, s1.Close())
	s2, err := Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

func TestAccountCRUD(t *testing.T) {
	s := newTestStore(t)
	repo := s.Bundle().Accounts
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	a := &store.Account{
		ID: "acc-1", Name: "alice", Role: store.RoleAdmin,
		LifecycleState: store.LifecycleCreated,
		CreatedAt:      now, UpdatedAt: now,
	}
	require.NoError(t, repo.Create(ctx, a))

	got, err := repo.Get(ctx, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Name)
	assert.Equal(t, store.RoleAdmin, got.Role)
	assert.WithinDuration(t, now, got.CreatedAt, time.Second)

	byName, err := repo.GetByName(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, "acc-1", byName.ID)

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got.Name = "alice2"
	got.UpdatedAt = now.Add(time.Hour)
	require.NoError(t, repo.Update(ctx, got))
	again, _ := repo.Get(ctx, "acc-1")
	assert.Equal(t, "alice2", again.Name)

	require.NoError(t, repo.Delete(ctx, "acc-1"))
	_, err = repo.Get(ctx, "acc-1")
	e, ok := errcode.As(err)
	require.True(t, ok)
	assert.Equal(t, errcode.NotFound, e.Code)
}

func TestAccountCreateDuplicateName(t *testing.T) {
	s := newTestStore(t)
	repo := s.Bundle().Accounts
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.Create(ctx, &store.Account{
		ID: "a", Name: "dup", Role: store.RoleUser, LifecycleState: store.LifecycleCreated,
		CreatedAt: now, UpdatedAt: now,
	}))
	err := repo.Create(ctx, &store.Account{
		ID: "b", Name: "dup", Role: store.RoleUser, LifecycleState: store.LifecycleCreated,
		CreatedAt: now, UpdatedAt: now,
	})
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestAccountInvalidEnums(t *testing.T) {
	s := newTestStore(t)
	repo := s.Bundle().Accounts
	ctx := context.Background()
	now := time.Now().UTC()
	err := repo.Create(ctx, &store.Account{
		ID: "x", Name: "x", Role: "wizard", LifecycleState: store.LifecycleCreated,
		CreatedAt: now, UpdatedAt: now,
	})
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestAccountListOrdered(t *testing.T) {
	s := newTestStore(t)
	repo := s.Bundle().Accounts
	ctx := context.Background()
	base := time.Now().UTC()
	for i, name := range []string{"a", "b", "c"} {
		require.NoError(t, repo.Create(ctx, &store.Account{
			ID: name, Name: name, Role: store.RoleUser, LifecycleState: store.LifecycleCreated,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			UpdatedAt: base.Add(time.Duration(i) * time.Second),
		}))
	}
	got, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "a", got[0].Name)
	assert.Equal(t, "b", got[1].Name)
	assert.Equal(t, "c", got[2].Name)
}

func TestTokenCRUD(t *testing.T) {
	s := newTestStore(t)
	accounts := s.Bundle().Accounts
	tokens := s.Bundle().Tokens
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, accounts.Create(ctx, &store.Account{
		ID: "a1", Name: "n1", Role: store.RoleUser, LifecycleState: store.LifecycleCreated,
		CreatedAt: now, UpdatedAt: now,
	}))

	tk := &store.Token{
		ID: "t1", AccountID: "a1", Hash: []byte("bcrypt-fake"),
		CreatedAt: now,
	}
	require.NoError(t, tokens.Create(ctx, tk))
	got, err := tokens.Get(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, "a1", got.AccountID)
	assert.False(t, got.Revoked())

	require.NoError(t, tokens.TouchLastUsed(ctx, "t1", now.Add(time.Minute)))
	again, _ := tokens.Get(ctx, "t1")
	require.NotNil(t, again.LastUsedAt)

	require.NoError(t, tokens.Revoke(ctx, "t1", now.Add(time.Hour)))
	revoked, _ := tokens.Get(ctx, "t1")
	assert.True(t, revoked.Revoked())

	// Second revoke is Conflict, not silent success.
	err = tokens.Revoke(ctx, "t1", now.Add(time.Hour))
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestTokenRevokeNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.Bundle().Tokens.Revoke(context.Background(), "nope", time.Now())
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.NotFound, e.Code)
}

func TestTokenListByAccount(t *testing.T) {
	s := newTestStore(t)
	accounts := s.Bundle().Accounts
	tokens := s.Bundle().Tokens
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, accounts.Create(ctx, &store.Account{
		ID: "a", Name: "a", Role: store.RoleUser, LifecycleState: store.LifecycleCreated,
		CreatedAt: now, UpdatedAt: now,
	}))
	for i, id := range []string{"t1", "t2", "t3"} {
		require.NoError(t, tokens.Create(ctx, &store.Token{
			ID: id, AccountID: "a", Hash: []byte("h"),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}
	got, err := tokens.ListByAccount(ctx, "a")
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestAuditRecordAndList(t *testing.T) {
	s := newTestStore(t)
	audit := s.Bundle().Audit
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i, action := range []string{"account.create", "token.create", "token.revoke"} {
		require.NoError(t, audit.Record(ctx, &store.AuditEntry{
			ID:        action,
			AccountID: "root",
			Action:    action,
			Target:    "x",
			Payload:   `{"i":` + string(rune('0'+i)) + `}`,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}

	all, err := audit.List(ctx, store.AuditFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 3)
	// Newest first.
	assert.Equal(t, "token.revoke", all[0].Action)

	filtered, err := audit.List(ctx, store.AuditFilter{AccountID: "nobody"})
	require.NoError(t, err)
	assert.Empty(t, filtered)

	since := now.Add(2 * time.Second)
	recent, err := audit.List(ctx, store.AuditFilter{Since: &since})
	require.NoError(t, err)
	assert.Len(t, recent, 1)
	assert.Equal(t, "token.revoke", recent[0].Action)

	limited, err := audit.List(ctx, store.AuditFilter{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestAuditListOrdersSameSecondByIDDescending(t *testing.T) {
	s := newTestStore(t)
	audit := s.Bundle().Audit
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, audit.Record(ctx, &store.AuditEntry{
		ID:        "00000000-0000-7000-8000-000000000001",
		AccountID: "root",
		Action:    "account.create",
		CreatedAt: now,
	}))
	require.NoError(t, audit.Record(ctx, &store.AuditEntry{
		ID:        "00000000-0000-7000-8000-000000000002",
		AccountID: "root",
		Action:    "account.update",
		CreatedAt: now,
	}))

	got, err := audit.List(ctx, store.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "account.update", got[0].Action)
	assert.Equal(t, "account.create", got[1].Action)
}

func TestAuditRecordRejectsEmptyAction(t *testing.T) {
	s := newTestStore(t)
	err := s.Bundle().Audit.Record(context.Background(), &store.AuditEntry{
		ID: "x", AccountID: "y", CreatedAt: time.Now().UTC(),
	})
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestForeignKeyCascadeOnAccountDelete(t *testing.T) {
	s := newTestStore(t)
	accounts := s.Bundle().Accounts
	tokens := s.Bundle().Tokens
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, accounts.Create(ctx, &store.Account{
		ID: "a", Name: "n", Role: store.RoleUser, LifecycleState: store.LifecycleCreated,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, tokens.Create(ctx, &store.Token{
		ID: "t", AccountID: "a", Hash: []byte("h"), CreatedAt: now,
	}))

	require.NoError(t, accounts.Delete(ctx, "a"))

	_, err := tokens.Get(ctx, "t")
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.NotFound, e.Code, "ON DELETE CASCADE should have removed token")
}
