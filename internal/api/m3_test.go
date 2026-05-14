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
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// m3Env extends the M2 testEnv with a mock-factory Connector so we can
// exercise the M3 set-discord/online/offline/status/debug surface
// without touching real Discord.
type m3Env struct {
	*testEnv
	conn    *connector.Connector
	mu      sync.Mutex
	created []*mock.Provider
	key     []byte
}

func (m *m3Env) latest() *mock.Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(m.testEnv.t, m.created, "no mock Providers built yet")
	return m.created[len(m.created)-1]
}

func newM3Env(t *testing.T) *m3Env {
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
	env.conn = connector.New(func(token string, hint bot.Identity) bot.Provider {
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
		Bundler:     s,
		Connector:   env.conn,
		MasterKey:   key,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	env.testEnv = &testEnv{
		t: t, server: srv, adminToken: raw, adminID: root.ID, store: s,
	}
	return env
}

func TestSetDiscordPersistsTokenAndAudits(t *testing.T) {
	env := newM3Env(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "agent1", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-token-1234"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// status now reports has_bot_token = true.
	resp, body = env.do(http.MethodGet, "/v1/accounts/"+a.ID+"/status", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var st apiv1.StatusResponse
	require.NoError(t, json.Unmarshal(body, &st))
	assert.True(t, st.HasBotToken)
	assert.Equal(t, "offline", st.ProviderStatus)

	// audit log contains account.discord_set.
	resp, body = env.do(http.MethodGet, "/v1/audit?account="+env.adminID, nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var au apiv1.AuditListResponse
	require.NoError(t, json.Unmarshal(body, &au))
	var found bool
	for _, e := range au.Entries {
		if e.Action == string(audit.ActionAccountSetDiscord) && e.Target == a.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "audit log should record account.discord_set")
}

func TestSetDiscordRejectsEmptyToken(t *testing.T) {
	env := newM3Env(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "a", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: ""}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
}

func TestOnlineWithoutTokenRejected(t *testing.T) {
	env := newM3Env(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "no-tok", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var env2 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env2))
	assert.Equal(t, string(errcode.InvalidArgument), env2.Error.Code)
}

func TestOnlineHappyPath(t *testing.T) {
	env := newM3Env(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "live", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))
	env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-token"}, env.adminToken)

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var st apiv1.StatusResponse
	require.NoError(t, json.Unmarshal(body, &st))
	assert.Equal(t, "online", st.Account.LifecycleState)
	assert.Equal(t, "online", st.ProviderStatus)
	require.NotNil(t, st.Identity)
	assert.Equal(t, "live", st.Identity.Username)

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/offline", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &st))
	assert.Equal(t, "offline", st.Account.LifecycleState)
	assert.Equal(t, "offline", st.ProviderStatus)
}

func TestDebugSendForwardsToProvider(t *testing.T) {
	env := newM3Env(t)
	a := bringOnline(t, env, "sender")
	resp, body := env.do(http.MethodPost, "/v1/debug/send",
		apiv1.DebugSendRequest{
			AccountID: a.ID,
			ChannelID: "ch-1",
			Content:   "hello world",
		}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out apiv1.DebugSendResponse
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, "hello world", out.Message.Content)
	assert.Equal(t, "ch-1", out.Message.ChannelID)

	// Mock recorded the send.
	p := env.latest()
	require.Len(t, p.Sent, 1)
	assert.Equal(t, "hello world", p.Sent[0].Content)
}

func TestDebugSendNotOnline(t *testing.T) {
	env := newM3Env(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "offline", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))

	resp, body = env.do(http.MethodPost, "/v1/debug/send",
		apiv1.DebugSendRequest{
			AccountID: a.ID,
			ChannelID: "ch-1",
			Content:   "x",
		}, env.adminToken)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestDebugEventsStreamsNDJSON(t *testing.T) {
	env := newM3Env(t)
	a := bringOnline(t, env, "watcher")

	// Open the streaming endpoint via raw http.Client.
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/v1/debug/events?account="+a.ID, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Inject a message via the mock, then assert we receive it.
	p := env.latest()
	go func() {
		time.Sleep(50 * time.Millisecond)
		p.InjectMessage(bot.Message{ID: "m1", ChannelID: "ch", AuthorID: "u-x", Content: "ping"})
	}()

	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read events line: %v", err)
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
		if ev["type"] == "message_new" {
			msg, _ := ev["message"].(map[string]any)
			assert.Equal(t, "ping", msg["content"])
			return // pass
		}
	}
	t.Fatal("never saw a message_new event")
}

// bringOnline creates an account, sets a fake bot token, and brings
// it online. Returns the account response.
func bringOnline(t *testing.T, env *m3Env, name string) apiv1.AccountResponse {
	t.Helper()
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: name, Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return a
}
