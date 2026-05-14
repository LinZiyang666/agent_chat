package client

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/account"
	"github.com/LinZiyang666/agentchat/internal/api"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// startTestDaemon spins up a real agentchatd-equivalent stack on a
// Unix socket inside the test's temp dir. Returns the socket path and
// a freshly issued admin token. Cleans up automatically.
func startTestDaemon(t *testing.T) (socketPath, token string) {
	t.Helper()
	dir := t.TempDir()
	socketPath = filepath.Join(dir, "d.sock")
	dbPath := filepath.Join(dir, "d.db")

	ctx := context.Background()
	s, err := sqlite.Open(ctx, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bundle := s.Bundle()
	svc := account.NewService(bundle.Accounts)
	rec := audit.NewRecorder(bundle.Audit)
	mgr := auth.NewManager(bundle.Tokens)

	root, created, err := svc.BootstrapRoot(ctx)
	require.NoError(t, err)
	require.True(t, created)
	raw, _, err := mgr.Issue(ctx, root.ID)
	require.NoError(t, err)

	router := api.NewRouter(api.Deps{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:    svc,
		AccountRepo: bundle.Accounts,
		TokenRepo:   bundle.Tokens,
		Auth:        mgr,
		Audit:       rec,
		Bundler:     s,
	})
	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: router}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	return socketPath, raw
}

func TestClientHealthz(t *testing.T) {
	sock, _ := startTestDaemon(t)
	c := New(sock, "")
	resp, err := c.Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Status)
}

func TestClientWhoami(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	w, err := c.Whoami(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "root", w.Account.Name)
}

func TestClientCreateAndListAccount(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "bob", "user")
	require.NoError(t, err)
	assert.Equal(t, "bob", a.Name)

	list, err := c.ListAccounts(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list), 2) // root + bob

	got, err := c.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "bob", got.Name)
}

func TestClientErrorMapping(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	_, err := c.GetAccount(context.Background(), "does-not-exist")
	require.Error(t, err)
	e, ok := errcode.As(err)
	require.True(t, ok)
	assert.Equal(t, errcode.NotFound, e.Code)
}

func TestClientUnreachable(t *testing.T) {
	c := New("/nonexistent/socket.sock", "")
	_, err := c.Healthz(context.Background())
	require.Error(t, err)
	e, ok := errcode.As(err)
	require.True(t, ok)
	assert.Equal(t, errcode.Unavailable, e.Code,
		"network failure must map to Unavailable so callers can distinguish")
}

func TestClientUpdateAndDelete(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "carol", "user")
	require.NoError(t, err)

	renamed, err := c.RenameAccount(context.Background(), a.ID, "carolyn")
	require.NoError(t, err)
	assert.Equal(t, "carolyn", renamed.Name)

	promoted, err := c.SetAccountRole(context.Background(), a.ID, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", promoted.Role)

	require.NoError(t, c.DeleteAccount(context.Background(), a.ID))
	_, err = c.GetAccount(context.Background(), a.ID)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.NotFound, e.Code)
}

func TestClientTokenLifecycle(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "dave", "admin")
	require.NoError(t, err)

	created, err := c.CreateToken(context.Background(), a.ID)
	require.NoError(t, err)
	require.NotEmpty(t, created.Raw)

	// Use the new token to authenticate.
	c2 := New(sock, created.Raw)
	w, err := c2.Whoami(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "dave", w.Account.Name)

	require.NoError(t, c.RevokeToken(context.Background(), created.Token.ID))
	_, err = c2.Whoami(context.Background())
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthRevoked, e.Code)

	list, err := c.ListTokens(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestClientAuditList(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	_, err := c.CreateAccount(context.Background(), "audit-1", "user")
	require.NoError(t, err)

	entries, err := c.ListAudit(context.Background(), ListAuditOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, entries)
}

func TestClientTimeout(t *testing.T) {
	sock, tok := startTestDaemon(t)
	c := New(sock, tok)
	c.SetTimeout(100 * time.Millisecond)
	// A normal call still works inside the timeout.
	_, err := c.Healthz(context.Background())
	assert.NoError(t, err)
}
