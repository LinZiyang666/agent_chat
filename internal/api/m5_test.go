package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	"github.com/LinZiyang666/agentchat/internal/message"
	"github.com/LinZiyang666/agentchat/internal/state"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// m5Env adds a wired-in state.Bus to the M4 test rig so the new
// /v1/state endpoints can be exercised end-to-end.
type m5Env struct {
	*testEnv
	conn     *connector.Connector
	ingester *message.Ingester
	bus      *state.Bus
	mu       sync.Mutex
	created  []*mock.Provider
	key      []byte
	prober   *mock.Prober
}

func (m *m5Env) latest() *mock.Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(m.testEnv.t, m.created, "no mock Providers built yet")
	return m.created[len(m.created)-1]
}

func newM5Env(t *testing.T) *m5Env {
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

	env := &m5Env{key: key}
	env.conn = connector.New(func(_ string, hint bot.Identity) bot.Provider {
		p := mock.New(bot.Identity{UserID: "u-" + hint.Username, Username: hint.Username})
		env.mu.Lock()
		env.created = append(env.created, p)
		env.mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { env.conn.Shutdown(context.Background()) })

	agg := state.NewFromConnector(bundle, env.conn)
	// Short debounce so the watch test doesn't have to wait 200ms.
	env.bus = state.NewBusWithDebounce(agg, slog.New(slog.NewTextHandler(io.Discard, nil)), 30*time.Millisecond)
	t.Cleanup(env.bus.Shutdown)

	env.ingester = message.New(env.conn, s, slog.New(slog.NewTextHandler(io.Discard, nil)), env.bus)

	env.prober = mock.NewProber()
	router := NewRouter(Deps{
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:       svc,
		AccountRepo:    bundle.Accounts,
		TokenRepo:      bundle.Tokens,
		Auth:           mgr,
		Audit:          rec,
		Bundler:        s,
		Connector:      env.conn,
		MasterKey:      key,
		Ingester:       env.ingester,
		StateBus:       env.bus,
		IdentityProber: env.prober,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	env.testEnv = &testEnv{
		t: t, server: srv, adminToken: raw, adminID: root.ID, store: s,
	}
	return env
}

func (env *m5Env) onlineAdminAndCreateRoom(t *testing.T, name string) apiv1.RoomResponse {
	t.Helper()
	resp, _ := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-root"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, body := env.do(http.MethodPost, "/v1/rooms", apiv1.CreateRoomRequest{Name: name}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))
	return room
}

func TestGetStateReturnsEmptySnapshotForBootstrapRoot(t *testing.T) {
	env := newM5Env(t)
	resp, body := env.do(http.MethodGet, "/v1/state", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	assert.Equal(t, env.adminID, snap["account_id"])
	totals, _ := snap["totals"].(map[string]any)
	require.NotNil(t, totals)
	assert.EqualValues(t, 0, totals["unread"])
}

func TestGetStateAfterSendShowsUnread(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "ops")
	// admin's own send marks themself "read" — to see unread we
	// need a viewer that's a subscribed member of the room and
	// did not send.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "viewer", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var viewer apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &viewer))
	// Bring viewer online so bot_user_id is captured (required by
	// InviteMember).
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-v"}, env.adminToken)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Invite viewer as subscribed.
	resp, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: viewer.ID, Subscribed: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Admin sends.
	resp, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "hey"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Issue a token for the viewer and check their state.
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))

	resp, body = env.do(http.MethodGet, "/v1/state", nil, tok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	totals := snap["totals"].(map[string]any)
	assert.EqualValues(t, 1, totals["unread"])
	rooms, _ := snap["rooms"].([]any)
	require.Len(t, rooms, 1)
	r0 := rooms[0].(map[string]any)
	assert.EqualValues(t, 1, r0["unread"])
}

func TestWatchStateEmitsInitialAndDebouncedFrames(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "live")
	// Need a viewer for unread to actually move.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "viewer", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var viewer apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &viewer))
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-v"}, env.adminToken)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: viewer.ID, Subscribed: true}, env.adminToken)

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))

	// Open the stream as viewer.
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/v1/state/watch", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok.Raw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streamResp, err := env.server.Client().Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer streamResp.Body.Close()
	require.Equal(t, http.StatusOK, streamResp.StatusCode)

	br := bufio.NewReader(streamResp.Body)
	// Initial frame.
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &first))
	firstVer := int64(first["version"].(float64))

	// Trigger a state change: admin sends, debouncer pushes a frame.
	go func() {
		_, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
			apiv1.SendMessageRequest{Content: "yo"}, env.adminToken)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err = br.ReadString('\n')
		if err != nil {
			break
		}
		var s map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &s))
		v := int64(s["version"].(float64))
		if v > firstVer {
			totals := s["totals"].(map[string]any)
			assert.EqualValues(t, 1, totals["unread"])
			return // pass
		}
	}
	t.Fatal("never saw a debounced state frame after send")
}

func TestWatchStateRejectsSinceParameter(t *testing.T) {
	// M5-P3-005 contract: since=<version> is reserved but not
	// implemented in M5 — we reject explicitly so callers don't
	// write code relying on resume semantics that don't exist yet.
	env := newM5Env(t)
	resp, body := env.do(http.MethodGet, "/v1/state/watch?since=1", nil, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	var env2 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env2))
	assert.Contains(t, env2.Error.Message, "since")
}

func TestWatchStateIdleEmitsNothing(t *testing.T) {
	env := newM5Env(t)
	// Open the stream as admin (bootstrap, no rooms, no traffic).
	// We deliberately use the parent context to open the stream and
	// only cancel mid-read — putting a 400ms request-context on the
	// whole call races with -race overhead, where http.Client.Do can
	// return "ctx deadline exceeded" before the response body is
	// ever readable (audit Finding B-1).
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/v1/state/watch", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streamResp, err := env.server.Client().Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer streamResp.Body.Close()
	require.Equal(t, http.StatusOK, streamResp.StatusCode)

	br := bufio.NewReader(streamResp.Body)
	// Initial frame arrives.
	_, err = br.ReadString('\n')
	require.NoError(t, err)

	// Idle for an 800ms window — should see zero additional frames.
	// Reader runs in a goroutine; we cancel the ctx to terminate the
	// stream and inspect what arrived.
	gotLine := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		gotLine <- line
	}()
	select {
	case extra := <-gotLine:
		t.Fatalf("idle stream emitted an extra frame: %q", extra)
	case <-time.After(800 * time.Millisecond):
		// pass — no second frame within the window.
	}
}
