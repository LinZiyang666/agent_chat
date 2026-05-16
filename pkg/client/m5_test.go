package client

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/account"
	"github.com/LinZiyang666/agentchat/internal/api"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/bot/mock"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/crypto"
	"github.com/LinZiyang666/agentchat/internal/message"
	"github.com/LinZiyang666/agentchat/internal/state"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// startM5TestDaemon — same shape as M4 rig but with state.Bus wired.
func startM5TestDaemon(t *testing.T) (sock, token string, latest func() *mock.Provider) {
	t.Helper()
	dir := t.TempDir()
	sock = filepath.Join(dir, "d.sock")

	s, err := sqlite.Open(context.Background(), filepath.Join(dir, "d.db"))
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
	token = raw

	key := make([]byte, crypto.MasterKeyLen)
	_, err = rand.Read(key)
	require.NoError(t, err)

	var (
		mu       sync.Mutex
		captured []*mock.Provider
	)
	conn := connector.New(func(_ string, hint bot.Identity) bot.Provider {
		p := mock.New(bot.Identity{UserID: "u-" + hint.Username, Username: hint.Username})
		mu.Lock()
		captured = append(captured, p)
		mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { conn.Shutdown(context.Background()) })

	agg := state.NewFromConnector(bundle, conn)
	bus := state.NewBusWithDebounce(agg, slog.New(slog.NewTextHandler(io.Discard, nil)), 30*time.Millisecond)
	t.Cleanup(bus.Shutdown)
	ing := message.New(conn, s, slog.New(slog.NewTextHandler(io.Discard, nil)), bus)

	router := api.NewRouter(api.Deps{
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:       svc,
		AccountRepo:    bundle.Accounts,
		TokenRepo:      bundle.Tokens,
		Auth:           mgr,
		Audit:          rec,
		Bundler:        s,
		Connector:      conn,
		MasterKey:      key,
		Ingester:       ing,
		StateBus:       bus,
		IdentityProber: mock.NewProber(),
	})
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: router}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	latest = func() *mock.Provider {
		mu.Lock()
		defer mu.Unlock()
		require.NotEmpty(t, captured, "no mock Providers built yet")
		return captured[len(captured)-1]
	}
	return sock, token, latest
}

func TestClientGetStateEmpty(t *testing.T) {
	sock, tok, _ := startM5TestDaemon(t)
	c := New(sock, tok)
	s, err := c.GetState(context.Background())
	require.NoError(t, err)
	require.NotNil(t, s)
	totals, _ := s["totals"].(map[string]any)
	require.NotNil(t, totals)
	assert.EqualValues(t, 0, totals["unread"])
}

func TestClientWatchStateRoundTrip(t *testing.T) {
	sock, tok, _ := startM5TestDaemon(t)
	c := New(sock, tok)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := c.WatchState(ctx)
	require.NoError(t, err)
	defer stream.Close()

	br := bufio.NewReader(stream)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	var snap map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &snap))
	assert.NotEmpty(t, snap["account_id"])
	assert.NotZero(t, snap["version"])
}
