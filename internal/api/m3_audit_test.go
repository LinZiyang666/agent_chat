package api

import (
	"context"
	"crypto/rand"
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
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/bot/mock"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/crypto"
	"github.com/LinZiyang666/agentchat/internal/store"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

func TestDeleteOnlineAccountDoesNotLeaveProviderOrphaned(t *testing.T) {
	env := newM3Env(t)
	a := bringOnline(t, env, "delete-live")
	_, ok := env.conn.Provider(a.ID)
	require.True(t, ok, "test setup should leave provider online")

	resp, body := env.do(http.MethodDelete, "/v1/accounts/"+a.ID, nil, env.adminToken)
	if resp.StatusCode == http.StatusConflict {
		_, ok = env.conn.Provider(a.ID)
		assert.True(t, ok, "rejected delete should keep the live provider intact")
		resp, body = env.do(http.MethodGet, "/v1/accounts/"+a.ID, nil, env.adminToken)
		assert.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		return
	}
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(body))

	_, ok = env.conn.Provider(a.ID)
	assert.False(t, ok, "deleting an account must tear down its live provider")

	resp, body = env.do(http.MethodPost, "/v1/debug/send", apiv1.DebugSendRequest{
		AccountID: a.ID,
		ChannelID: "ch-1",
		Content:   "should not send after delete",
	}, env.adminToken)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
}

type failActionAuditRepo struct {
	inner      store.AuditRepo
	failAction string
}

func (r failActionAuditRepo) Record(ctx context.Context, e *store.AuditEntry) error {
	if e != nil && e.Action == r.failAction {
		return errors.New("audit storage offline for " + r.failAction)
	}
	return r.inner.Record(ctx, e)
}

func (r failActionAuditRepo) List(ctx context.Context, f store.AuditFilter) ([]*store.AuditEntry, error) {
	return r.inner.List(ctx, f)
}

type auditOverrideBundler struct {
	inner      store.Bundler
	failAction string
}

func (b auditOverrideBundler) WithTx(ctx context.Context, fn func(store.Bundle) error) error {
	return b.inner.WithTx(ctx, func(bundle store.Bundle) error {
		bundle.Audit = failActionAuditRepo{inner: bundle.Audit, failAction: b.failAction}
		return fn(bundle)
	})
}

func TestOfflineAuditFailureKeepsProviderOnline(t *testing.T) {
	env := newM3EnvWithAuditOverride(t, string(audit.ActionAccountOffline))
	a := bringOnline(t, env, "offline-audit-fail")
	_, ok := env.conn.Provider(a.ID)
	require.True(t, ok, "test setup should leave provider online")

	resp, body := env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/offline", nil, env.adminToken)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode, string(body))

	_, ok = env.conn.Provider(a.ID)
	assert.True(t, ok, "failed offline transaction must not disconnect the live provider")

	resp, body = env.do(http.MethodGet, "/v1/accounts/"+a.ID+"/status", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var st apiv1.StatusResponse
	require.NoError(t, json.Unmarshal(body, &st))
	assert.Equal(t, "online", st.Account.LifecycleState)
	assert.Equal(t, "online", st.ProviderStatus)
}

func newM3EnvWithAuditOverride(t *testing.T, failAction string) *m3Env {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bundle := s.Bundle()
	svc := account.NewService(bundle.Accounts)
	rec := audit.NewRecorder(bundle.Audit)
	mgr := auth.NewManager(bundle.Tokens)

	root, _, err := svc.BootstrapRoot(context.Background())
	require.NoError(t, err)
	raw, _, err := mgr.Issue(context.Background(), root.ID)
	require.NoError(t, err)

	key := make([]byte, crypto.MasterKeyLen)
	_, err = rand.Read(key)
	require.NoError(t, err)

	env := &m3Env{key: key}
	env.conn = connector.New(func(_ string, hint bot.Identity) bot.Provider {
		p := mock.New(bot.Identity{UserID: "u-" + hint.Username, Username: hint.Username})
		env.mu.Lock()
		env.created = append(env.created, p)
		env.mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { env.conn.Shutdown(context.Background()) })

	router := NewRouter(Deps{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:    svc,
		AccountRepo: bundle.Accounts,
		TokenRepo:   bundle.Tokens,
		Auth:        mgr,
		Audit:       rec,
		Bundler: auditOverrideBundler{
			inner:      s,
			failAction: failAction,
		},
		Connector: env.conn,
		MasterKey: key,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	env.testEnv = &testEnv{
		t: t, server: srv, adminToken: raw, adminID: root.ID, store: s,
	}
	return env
}
