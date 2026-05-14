package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/account"
	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// brokenAuditRepo simulates a downed audit backend.
type brokenAuditRepo struct{}

func (brokenAuditRepo) Record(_ context.Context, _ *store.AuditEntry) error {
	return errors.New("audit storage offline")
}

func (brokenAuditRepo) List(_ context.Context, _ store.AuditFilter) ([]*store.AuditEntry, error) {
	return nil, nil
}

// brokenAuditBundler wraps a real store.Bundler but replaces the
// transaction-scoped Audit repo with brokenAuditRepo. Used to prove
// that a failing audit insert rolls back the sibling mutation
// (M2-P3-012 fix).
type brokenAuditBundler struct {
	inner store.Bundler
}

func (b *brokenAuditBundler) WithTx(ctx context.Context, fn func(store.Bundle) error) error {
	return b.inner.WithTx(ctx, func(bundle store.Bundle) error {
		bundle.Audit = brokenAuditRepo{}
		return fn(bundle)
	})
}

// testEnv is a fully wired daemon-side stack pointed at a temp sqlite DB.
type testEnv struct {
	t          *testing.T
	server     *httptest.Server
	adminToken string
	adminID    string
	store      *sqlite.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bundle := s.Bundle()
	svc := account.NewService(bundle.Accounts)
	rec := audit.NewRecorder(bundle.Audit)
	mgr := auth.NewManager(bundle.Tokens)

	// Bootstrap an admin and a token for it.
	root, created, err := svc.BootstrapRoot(context.Background())
	require.NoError(t, err)
	require.True(t, created)
	raw, _, err := mgr.Issue(context.Background(), root.ID)
	require.NoError(t, err)

	router := NewRouter(Deps{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:    svc,
		AccountRepo: bundle.Accounts,
		TokenRepo:   bundle.Tokens,
		Auth:        mgr,
		Audit:       rec,
		Bundler:     s,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &testEnv{t: t, server: srv, adminToken: raw, adminID: root.ID, store: s}
}

func (e *testEnv) do(method, path string, body any, token string) (*http.Response, []byte) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(e.t, err)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.server.URL+path, rdr)
	require.NoError(e.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.server.Client().Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func unmarshalErr(t *testing.T, body []byte) apiv1.ErrorEnvelope {
	t.Helper()
	var e apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &e))
	return e
}

func TestHealthz(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodGet, "/v1/healthz", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var h apiv1.HealthzResponse
	require.NoError(t, json.Unmarshal(body, &h))
	assert.Equal(t, "ok", h.Status)
}

func TestUnauthorizedWithoutToken(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodGet, "/v1/whoami", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	e := unmarshalErr(t, body)
	assert.Equal(t, string(errcode.AuthMissing), e.Error.Code)
}

func TestWhoami(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodGet, "/v1/whoami", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var w apiv1.WhoamiResponse
	require.NoError(t, json.Unmarshal(body, &w))
	assert.Equal(t, "root", w.Account.Name)
	assert.NotEmpty(t, w.TokenID)
}

func TestCreateAccountHappy(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "alice", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))
	assert.Equal(t, "alice", a.Name)
	assert.Equal(t, "user", a.Role)
}

func TestCreateAccountConflict(t *testing.T) {
	env := newTestEnv(t)
	env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "dup", Role: "user"}, env.adminToken)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "dup", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	e := unmarshalErr(t, body)
	assert.Equal(t, string(errcode.Conflict), e.Error.Code)
}

func TestCreateAccountInvalidRole(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "x", Role: "wizard"}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	e := unmarshalErr(t, body)
	assert.Equal(t, string(errcode.InvalidArgument), e.Error.Code)
}

func TestRBACUserCannotCreateAccount(t *testing.T) {
	env := newTestEnv(t)
	// Create a user account + a token for it.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "bob", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var bob apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &bob))

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+bob.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var ct apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &ct))

	// Try to create another account using bob's user-level token.
	resp, body = env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "carol", Role: "user"}, ct.Raw)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	e := unmarshalErr(t, body)
	assert.Equal(t, string(errcode.PermDenied), e.Error.Code)
}

func TestUpdateAccountRename(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "old", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))
	newName := "new"
	resp, _ = env.do(http.MethodPatch, "/v1/accounts/"+a.ID,
		apiv1.UpdateAccountRequest{Name: &newName}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestUpdateAccountNoFields(t *testing.T) {
	env := newTestEnv(t)
	resp, _ := env.do(http.MethodPatch, "/v1/accounts/"+env.adminID,
		apiv1.UpdateAccountRequest{}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestDeleteAccount(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "ephemeral", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))
	resp, _ = env.do(http.MethodDelete, "/v1/accounts/"+a.ID, nil, env.adminToken)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp, _ = env.do(http.MethodGet, "/v1/accounts/"+a.ID, nil, env.adminToken)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestTokenLifecycle(t *testing.T) {
	env := newTestEnv(t)
	// Issue + verify.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "tokuser", Role: "admin"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &created))
	require.NotEmpty(t, created.Raw)

	// List tokens.
	resp, body = env.do(http.MethodGet, "/v1/accounts/"+a.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list apiv1.TokenListResponse
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Len(t, list.Tokens, 1)

	// New token can authenticate as the new account.
	resp, body = env.do(http.MethodGet, "/v1/whoami", nil, created.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var w apiv1.WhoamiResponse
	require.NoError(t, json.Unmarshal(body, &w))
	assert.Equal(t, "tokuser", w.Account.Name)

	// Revoke.
	resp, _ = env.do(http.MethodDelete, "/v1/tokens/"+created.Token.ID, nil, env.adminToken)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Old token is now revoked.
	resp, body = env.do(http.MethodGet, "/v1/whoami", nil, created.Raw)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	e := unmarshalErr(t, body)
	assert.Equal(t, string(errcode.AuthRevoked), e.Error.Code)
}

func TestAuditTrail(t *testing.T) {
	env := newTestEnv(t)
	// Generate some auditable actions.
	env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "aaa", Role: "user"}, env.adminToken)
	env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "bbb", Role: "admin"}, env.adminToken)

	resp, body := env.do(http.MethodGet, "/v1/audit", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.GreaterOrEqual(t, len(out.Entries), 2)
	// Newest first; "bbb" was created after "aaa".
	first := out.Entries[0]
	assert.Equal(t, "account.create", first.Action)
}

func TestAuditFilters(t *testing.T) {
	env := newTestEnv(t)
	env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "x1", Role: "user"}, env.adminToken)
	resp, body := env.do(http.MethodGet, "/v1/audit?account="+env.adminID+"&limit=1", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &out))
	assert.LessOrEqual(t, len(out.Entries), 1)
}

func TestUnknownFieldRejected(t *testing.T) {
	env := newTestEnv(t)
	resp, _ := env.do(http.MethodPost, "/v1/accounts",
		map[string]any{"name": "x", "role": "user", "extra": "junk"}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestPATCHAtomicityInvalidRoleNoRename locks the HTTP-level part of
// M2-P3-002: a request with valid name + invalid role returns 400 and
// the account's name is unchanged on the next GET.
func TestPATCHAtomicityInvalidRoleNoRename(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "atomic", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	newName := "atomic-renamed"
	bogusRole := "wizard"
	resp, _ = env.do(http.MethodPatch, "/v1/accounts/"+a.ID,
		apiv1.UpdateAccountRequest{Name: &newName, Role: &bogusRole}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, body = env.do(http.MethodGet, "/v1/accounts/"+a.ID, nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "atomic", got.Name, "name must not change on 400 response")
}

// TestPATCHRenameIsAudited locks the M2-P3-003 fix: a rename-only
// update must land an account.update entry in the audit log.
func TestPATCHRenameIsAudited(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "to-rename", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodGet, "/v1/audit?account="+env.adminID, nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var beforeList apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &beforeList))
	before := len(beforeList.Entries)

	newName := "renamed"
	resp, _ = env.do(http.MethodPatch, "/v1/accounts/"+a.ID,
		apiv1.UpdateAccountRequest{Name: &newName}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body = env.do(http.MethodGet, "/v1/audit?account="+env.adminID, nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var afterList apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &afterList))
	require.Equal(t, before+1, len(afterList.Entries))
	assert.Equal(t, "account.update", afterList.Entries[0].Action)
	assert.Contains(t, afterList.Entries[0].Payload, "name")
	assert.Contains(t, afterList.Entries[0].Payload, "renamed")
}

// brokenAuditOnceRepo fails Record exactly once (on the request under
// test) and lets BootstrapRoot's pre-test audit-less seed go through.
// In M2 BootstrapRoot does NOT write audit so a plain brokenAuditRepo
// would be fine; we keep this struct for future-proofing.

// TestAuditFailureRollsBackMutation locks the M2-P3-012 fix: when
// audit storage is offline, the account-create mutation must roll
// back, so the ghost account is NOT present in the DB after the 500.
func TestAuditFailureRollsBackMutation(t *testing.T) {
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bundle := s.Bundle()
	svc := account.NewService(bundle.Accounts)
	mgr := auth.NewManager(bundle.Tokens)
	rec := audit.NewRecorder(brokenAuditRepo{}) // <- audit Record fails

	root, _, err := svc.BootstrapRoot(context.Background())
	require.NoError(t, err)
	raw, _, err := mgr.Issue(context.Background(), root.ID)
	require.NoError(t, err)

	router := NewRouter(Deps{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:    svc,
		AccountRepo: bundle.Accounts,
		TokenRepo:   bundle.Tokens,
		Auth:        mgr,
		Audit:       rec,
		Bundler:     &brokenAuditBundler{inner: s}, // intercept tx and break the audit repo
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	body, _ := json.Marshal(apiv1.CreateAccountRequest{Name: "ghost", Role: "user"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/accounts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"audit failure must surface as 500")

	// CRITICAL: the failed audit insert must have rolled back the
	// preceding account insert. No "ghost" account should exist.
	_, getErr := bundle.Accounts.GetByName(context.Background(), "ghost")
	require.Error(t, getErr)
	e, _ := errcode.As(getErr)
	require.NotNil(t, e)
	assert.Equal(t, errcode.NotFound, e.Code,
		"transactional rollback should leave no orphan account")
}

// TestPATCHNoopProducesNoAudit confirms that a no-op update (every
// field already matching) does not pollute the audit log.
func TestPATCHNoopProducesNoAudit(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "noop", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodGet, "/v1/audit?account="+env.adminID, nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var beforeList apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &beforeList))
	before := len(beforeList.Entries)

	sameName := "noop"
	sameRole := "user"
	resp, _ = env.do(http.MethodPatch, "/v1/accounts/"+a.ID,
		apiv1.UpdateAccountRequest{Name: &sameName, Role: &sameRole}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body = env.do(http.MethodGet, "/v1/audit?account="+env.adminID, nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var afterList apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &afterList))
	assert.Equal(t, before, len(afterList.Entries), "no-op update must not write audit row")
}
