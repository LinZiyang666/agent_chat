package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// setup builds a real sqlite store + a Manager + the test account.
func setup(t *testing.T) (*sqlite.Store, *Manager, *store.Account) {
	t.Helper()
	dir := t.TempDir()
	s, err := sqlite.Open(context.Background(), filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	acc := &store.Account{
		ID: "acc-1", Name: "alice", Role: store.RoleAdmin,
		LifecycleState: store.LifecycleCreated,
		CreatedAt:      now, UpdatedAt: now,
	}
	require.NoError(t, s.Bundle().Accounts.Create(context.Background(), acc))

	mgr := NewManager(s.Bundle().Tokens)
	return s, mgr, acc
}

func TestEncodeParseRoundTrip(t *testing.T) {
	raw := encodeRaw("abc123", "secret-secret")
	id, sec, err := parseRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, "abc123", id)
	assert.Equal(t, "secret-secret", sec)
}

func TestParseRawRejectsBadShapes(t *testing.T) {
	cases := []string{
		"",
		"agch_",
		"agch_noid",
		"agch__nosecret",
		"agch_id_",
		"nope_id_secret",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := parseRaw(raw)
			require.Error(t, err)
			e, _ := errcode.As(err)
			require.NotNil(t, e)
			assert.Equal(t, errcode.AuthInvalid, e.Code)
		})
	}
}

func TestManagerIssueAndVerify(t *testing.T) {
	_, mgr, acc := setup(t)
	ctx := context.Background()

	raw, tk, err := mgr.Issue(ctx, acc.ID)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.NotNil(t, tk)
	assert.Equal(t, acc.ID, tk.AccountID)

	gotAcc, gotTok, err := mgr.Verify(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, acc.ID, gotAcc)
	assert.Equal(t, tk.ID, gotTok)
}

func TestManagerVerifyWrongSecret(t *testing.T) {
	_, mgr, acc := setup(t)
	ctx := context.Background()
	raw, _, err := mgr.Issue(ctx, acc.ID)
	require.NoError(t, err)
	// Flip a character in the secret portion.
	tampered := raw[:len(raw)-1] + "X"
	if raw[len(raw)-1] == 'X' {
		tampered = raw[:len(raw)-1] + "Y"
	}
	_, _, err = mgr.Verify(ctx, tampered)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthInvalid, e.Code)
}

func TestManagerVerifyRevokedToken(t *testing.T) {
	_, mgr, acc := setup(t)
	ctx := context.Background()
	raw, tk, err := mgr.Issue(ctx, acc.ID)
	require.NoError(t, err)
	require.NoError(t, mgr.Revoke(ctx, tk.ID))
	_, _, err = mgr.Verify(ctx, raw)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthRevoked, e.Code)
}

func TestManagerVerifyUnknownToken(t *testing.T) {
	_, mgr, _ := setup(t)
	_, _, err := mgr.Verify(context.Background(),
		encodeRaw("00000000000000000000000000000000", "deadbeef"))
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthInvalid, e.Code, "should not leak NotFound")
}

func TestManagerIssueRejectsEmptyAccount(t *testing.T) {
	_, mgr, _ := setup(t)
	_, _, err := mgr.Issue(context.Background(), "")
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestManagerTouchesLastUsed(t *testing.T) {
	s, mgr, acc := setup(t)
	ctx := context.Background()
	raw, tk, err := mgr.Issue(ctx, acc.ID)
	require.NoError(t, err)

	preVerify, err := s.Bundle().Tokens.Get(ctx, tk.ID)
	require.NoError(t, err)
	assert.Nil(t, preVerify.LastUsedAt)

	_, _, err = mgr.Verify(ctx, raw)
	require.NoError(t, err)
	postVerify, err := s.Bundle().Tokens.Get(ctx, tk.ID)
	require.NoError(t, err)
	require.NotNil(t, postVerify.LastUsedAt)
}

// --- Middleware tests ---

// nullErrorWriter just records the most recent error so tests can
// inspect it without going through JSON encoding.
type nullErrorWriter struct{ last error }

func (n *nullErrorWriter) write(w http.ResponseWriter, err error) {
	n.last = err
	w.WriteHeader(errcode.HTTPStatus(err))
}

func newProtectedHandler(t *testing.T) (mgr *Manager, repo store.AccountRepo, h http.Handler, ew *nullErrorWriter, acc *store.Account) {
	t.Helper()
	s, m, a := setup(t)
	repo = s.Bundle().Accounts
	ew = &nullErrorWriter{}
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := AccountFromContext(r.Context())
		require.True(t, ok)
		assert.Equal(t, "alice", got.Name)
		w.WriteHeader(http.StatusOK)
	})
	mw := NewMiddleware(m, repo, ew.write)
	return m, repo, mw.Handler(final), ew, a
}

func TestMiddlewareAllowsValidBearer(t *testing.T) {
	mgr, _, h, ew, acc := newProtectedHandler(t)
	raw, _, err := mgr.Issue(context.Background(), acc.ID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, ew.last)
}

func TestMiddlewareRejectsMissingHeader(t *testing.T) {
	_, _, h, ew, _ := newProtectedHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	e, _ := errcode.As(ew.last)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthMissing, e.Code)
}

func TestMiddlewareRejectsNonBearer(t *testing.T) {
	_, _, h, ew, _ := newProtectedHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic xyz")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	e, _ := errcode.As(ew.last)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthInvalid, e.Code)
}

func TestMiddlewareRejectsBadToken(t *testing.T) {
	_, _, h, ew, _ := newProtectedHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	e, _ := errcode.As(ew.last)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthInvalid, e.Code)
}

func TestRequireAdmin(t *testing.T) {
	now := time.Now().UTC()
	admin := &store.Account{ID: "a", Role: store.RoleAdmin, Name: "a",
		LifecycleState: store.LifecycleCreated, CreatedAt: now, UpdatedAt: now}
	user := &store.Account{ID: "u", Role: store.RoleUser, Name: "u",
		LifecycleState: store.LifecycleCreated, CreatedAt: now, UpdatedAt: now}

	ew := &nullErrorWriter{}
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { innerCalled = true; w.WriteHeader(http.StatusOK) })
	h := RequireAdmin(ew.write, inner)

	t.Run("admin allowed", func(t *testing.T) {
		innerCalled = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(WithAccount(req.Context(), admin, "tok"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, innerCalled)
	})

	t.Run("user rejected", func(t *testing.T) {
		innerCalled = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(WithAccount(req.Context(), user, "tok"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, innerCalled)
		e, _ := errcode.As(ew.last)
		require.NotNil(t, e)
		assert.Equal(t, errcode.PermDenied, e.Code)
	})

	t.Run("missing context rejected", func(t *testing.T) {
		innerCalled = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, innerCalled)
	})
}
