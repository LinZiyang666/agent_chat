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
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// startM3TestDaemon stands up a daemon stack with a mock-factory
// Connector. Returns socket path, admin token, and a closure that
// hands back the most recently constructed mock Provider so tests
// can drive events without touching real Discord.
func startM3TestDaemon(t *testing.T) (sock, token string, latestProvider func() *mock.Provider) {
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
	conn := connector.New(func(token string, hint bot.Identity) bot.Provider {
		p := mock.New(bot.Identity{UserID: "u-" + hint.Username, Username: hint.Username})
		mu.Lock()
		captured = append(captured, p)
		mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { conn.Shutdown(context.Background()) })

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

	latestProvider = func() *mock.Provider {
		mu.Lock()
		defer mu.Unlock()
		require.NotEmpty(t, captured, "no mock Providers built yet")
		return captured[len(captured)-1]
	}
	return sock, token, latestProvider
}

func TestClientSetDiscordAndStatus(t *testing.T) {
	sock, tok, _ := startM3TestDaemon(t)
	c := New(sock, tok)

	a, err := c.CreateAccount(context.Background(), "agent", "user")
	require.NoError(t, err)

	resp, err := c.SetDiscord(context.Background(), a.ID, "fake-token")
	require.NoError(t, err)
	assert.Equal(t, a.ID, resp.ID)

	st, err := c.Status(context.Background(), a.ID)
	require.NoError(t, err)
	assert.True(t, st.HasBotToken)
	assert.Equal(t, "offline", st.ProviderStatus)
}

func TestClientOnlineOfflineLifecycle(t *testing.T) {
	sock, tok, _ := startM3TestDaemon(t)
	c := New(sock, tok)

	a, err := c.CreateAccount(context.Background(), "alive", "user")
	require.NoError(t, err)
	_, err = c.SetDiscord(context.Background(), a.ID, "fake")
	require.NoError(t, err)

	st, err := c.Online(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "online", st.Account.LifecycleState)
	assert.Equal(t, "online", st.ProviderStatus)
	require.NotNil(t, st.Identity)

	st, err = c.Offline(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "offline", st.Account.LifecycleState)
}

func TestClientOnlineWithoutTokenInvalid(t *testing.T) {
	sock, tok, _ := startM3TestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "no-tok", "user")
	require.NoError(t, err)
	_, err = c.Online(context.Background(), a.ID)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestClientDebugSend(t *testing.T) {
	sock, tok, latest := startM3TestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "sender", "user")
	require.NoError(t, err)
	_, err = c.SetDiscord(context.Background(), a.ID, "fake")
	require.NoError(t, err)
	_, err = c.Online(context.Background(), a.ID)
	require.NoError(t, err)

	resp, err := c.DebugSend(context.Background(), a.ID, "ch-1", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello", resp.Message.Content)
	assert.Equal(t, "ch-1", resp.Message.ChannelID)

	p := latest()
	require.Len(t, p.Sent, 1)
}

func TestClientStreamEvents(t *testing.T) {
	sock, tok, latest := startM3TestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "watch", "user")
	require.NoError(t, err)
	_, err = c.SetDiscord(context.Background(), a.ID, "fake")
	require.NoError(t, err)
	_, err = c.Online(context.Background(), a.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evCh, err := c.StreamEvents(ctx, a.ID)
	require.NoError(t, err)

	go func() {
		time.Sleep(50 * time.Millisecond)
		latest().InjectMessage(bot.Message{ID: "m1", ChannelID: "ch", AuthorID: "u-x", Content: "hi"})
	}()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-evCh:
			if !ok {
				t.Fatal("event stream closed before seeing message_new")
			}
			if ev.Type == "message_new" {
				return
			}
			// could be connected first; keep waiting
		case <-deadline:
			t.Fatal("timeout waiting for message_new event")
		}
	}
}

// TestClientDebugSendNotOnline exercises the error envelope decoding
// path on the streaming-friendly endpoint.
func TestClientDebugSendNotOnline(t *testing.T) {
	sock, tok, _ := startM3TestDaemon(t)
	c := New(sock, tok)
	a, err := c.CreateAccount(context.Background(), "off", "user")
	require.NoError(t, err)
	_, err = c.DebugSend(context.Background(), a.ID, "ch-1", "x")
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.Conflict, e.Code)
}

// Compile-time guard that NDJSON parsing in the test still works even
// when the stream is interleaved with multiple events.
func TestNDJSONFraming(t *testing.T) {
	body := `{"type":"connected","identity":{"user_id":"u","username":"a"}}
{"type":"message_new","message":{"id":"m1","channel_id":"c","author_id":"u","content":"hi","created_at":"2026-05-13T00:00:00Z"}}
`
	br := bufio.NewReader(stringReader(body))
	var events []Event
	dec := json.NewDecoder(br)
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			break
		}
		events = append(events, ev)
	}
	require.Len(t, events, 2)
	assert.Equal(t, "connected", events[0].Type)
	assert.Equal(t, "message_new", events[1].Type)
}

// stringReader is a tiny in-memory io.Reader for the parsing test.
type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	n := copy(p, []byte(s))
	if n < len(s) {
		return n, nil
	}
	return n, io.EOF
}
