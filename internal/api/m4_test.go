package api

import (
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
	"github.com/LinZiyang666/agentchat/internal/message"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// m4Env extends the M3 test rig with a wired-in Ingester so the
// rooms/messages routes can exercise the inbound persistence path.
type m4Env struct {
	*testEnv
	conn     *connector.Connector
	ingester *message.Ingester
	mu       sync.Mutex
	created  []*mock.Provider
	key      []byte
}

func (m *m4Env) latest() *mock.Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(m.testEnv.t, m.created, "no mock Providers built yet")
	return m.created[len(m.created)-1]
}

func newM4Env(t *testing.T) *m4Env {
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

	env := &m4Env{key: key}
	env.conn = connector.New(func(token string, hint bot.Identity) bot.Provider {
		p := mock.New(bot.Identity{UserID: "u-" + hint.Username, Username: hint.Username})
		env.mu.Lock()
		env.created = append(env.created, p)
		env.mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { env.conn.Shutdown(context.Background()) })

	env.ingester = message.New(env.conn, s, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

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
		Ingester:    env.ingester,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	env.testEnv = &testEnv{
		t: t, server: srv, adminToken: raw, adminID: root.ID, store: s,
	}
	return env
}

// onlineAccount brings the named account online (creates it as admin
// or user depending on the role, sets a fake bot token, runs online).
// Returns the account response.
func (env *m4Env) onlineAccount(t *testing.T, name, role string) apiv1.AccountResponse {
	t.Helper()
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: name, Role: role}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var a apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &a))
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-" + name}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+a.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return a
}

// issueToken issues a fresh API token for the given account.
func (env *m4Env) issueToken(t *testing.T, accountID string) string {
	t.Helper()
	resp, body := env.do(http.MethodPost, "/v1/accounts/"+accountID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var out apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &out))
	return out.Raw
}

func TestRoomCreateInsertsRowAndCallsCreateChannel(t *testing.T) {
	env := newM4Env(t)
	// Root (the admin) must be online to call CreateChannel; the M3
	// bootstrap root doesn't have a token. Set one and online it.
	resp, _ := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-root"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "ops"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var r apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &r))
	assert.Equal(t, "ops", r.Name)
	assert.Equal(t, "ch-ops", r.DiscordChannelID)

	// Mock recorded the channel creation.
	p := env.latest()
	require.Equal(t, []string{"ch-ops"}, p.Created)
}

func TestRoomCreateWithoutOnlineExecutorFails(t *testing.T) {
	env := newM4Env(t)
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "x"}, env.adminToken)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
}

func TestRoomListAdminSeesAllUserSeesOwn(t *testing.T) {
	env := newM4Env(t)
	// Online the admin so we can create rooms.
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)

	// Create two rooms.
	for _, n := range []string{"alpha", "beta"} {
		resp, _ := env.do(http.MethodPost, "/v1/rooms",
			apiv1.CreateRoomRequest{Name: n}, env.adminToken)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
	}

	// Admin: two rooms.
	resp, body := env.do(http.MethodGet, "/v1/rooms", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list apiv1.RoomListResponse
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Rooms, 2)

	// New user, no memberships: empty list.
	user := env.onlineAccount(t, "alice", "user")
	utok := env.issueToken(t, user.ID)
	resp, body = env.do(http.MethodGet, "/v1/rooms", nil, utok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Len(t, list.Rooms, 0)
}

func TestRoomInviteAndKickRoundTrip(t *testing.T) {
	env := newM4Env(t)
	// Online admin + create room.
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-root"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	adminProvider := env.latest()
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "ops"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))

	// Bring a target user online so it captures a bot_user_id.
	target := env.onlineAccount(t, "alice", "user")

	// Invite alice — subscribed.
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: target.ID, Subscribed: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var m apiv1.MembershipResponse
	require.NoError(t, json.Unmarshal(body, &m))
	assert.Equal(t, target.ID, m.AccountID)
	assert.True(t, m.Subscribed)

	// Mock recorded the per-channel grant.
	assert.Contains(t, adminProvider.Added, [2]string{"ch-ops", "u-alice"})

	// Kick alice — DELETE returns 204.
	resp, _ = env.do(http.MethodDelete, "/v1/rooms/"+room.ID+"/members/"+target.ID, nil, env.adminToken)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Contains(t, adminProvider.Removed, [2]string{"ch-ops", "u-alice"})
}

func TestRoomSendMessagePersistsAndDedupesEcho(t *testing.T) {
	env := newM4Env(t)
	// admin online + create room + admin sends.
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "general"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))

	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "hello world"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var sent apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Equal(t, "hello world", sent.Content)
	assert.NotEmpty(t, sent.DiscordMsgID)
	assert.Equal(t, "normal", sent.Priority)

	// Mock should have one Sent entry.
	require.Len(t, env.latest().Sent, 1)

	// Inject the same message back through the gateway — ingester
	// should see the UNIQUE conflict on discord_msg_id and skip.
	env.latest().InjectMessage(bot.Message{
		ID:        sent.DiscordMsgID,
		ChannelID: room.DiscordChannelID,
		AuthorID:  "u-root", // same as the bot that sent it
		Content:   "hello world",
		CreatedAt: time.Now(),
	})
	// Give the ingester goroutine a moment.
	time.Sleep(150 * time.Millisecond)

	resp, body = env.do(http.MethodGet, "/v1/rooms/"+room.ID+"/messages", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list apiv1.MessageListResponse
	require.NoError(t, json.Unmarshal(body, &list))
	// One message — the echo did NOT create a duplicate.
	assert.Len(t, list.Messages, 1)
}

func TestIngesterIngestsExternalMessage(t *testing.T) {
	env := newM4Env(t)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "g"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))

	// External Discord user (no agentchat account) sends a message.
	env.latest().InjectMessage(bot.Message{
		ID:        "outside-1",
		ChannelID: room.DiscordChannelID,
		AuthorID:  "u-external",
		Content:   "from outside",
		CreatedAt: time.Now(),
	})

	require.Eventually(t, func() bool {
		resp, body := env.do(http.MethodGet, "/v1/rooms/"+room.ID+"/messages", nil, env.adminToken)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var list apiv1.MessageListResponse
		_ = json.Unmarshal(body, &list)
		return len(list.Messages) == 1 && list.Messages[0].Content == "from outside"
	}, 10*time.Second, 100*time.Millisecond, "ingester should persist external message_new")
}

func TestMembershipPatchUserSelfService(t *testing.T) {
	env := newM4Env(t)
	// admin online + create room.
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "f"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "r"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))

	user := env.onlineAccount(t, "u1", "user")
	utok := env.issueToken(t, user.ID)

	// Invite — unsubscribed initially.
	_, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: user.ID, Subscribed: false}, env.adminToken)

	// User self-subscribes.
	resp, body = env.do(http.MethodPatch, "/v1/memberships/"+room.ID,
		apiv1.UpdateMembershipRequest{Subscribed: true}, utok)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var m apiv1.MembershipResponse
	require.NoError(t, json.Unmarshal(body, &m))
	assert.True(t, m.Subscribed)
}

func TestSendMessageRejectedForNonMember(t *testing.T) {
	env := newM4Env(t)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "f"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "r"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))

	user := env.onlineAccount(t, "stranger", "user")
	utok := env.issueToken(t, user.ID)

	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "intrusion"}, utok)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, string(body))
	var env2 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env2))
	assert.Equal(t, string(errcode.PermDenied), env2.Error.Code)
}

func TestMarkReadAndReplyAck(t *testing.T) {
	env := newM4Env(t)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "f"}, env.adminToken)
	_, _ = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	resp, body := env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: "r"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))

	user := env.onlineAccount(t, "u2", "user")
	utok := env.issueToken(t, user.ID)
	_, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: user.ID, Subscribed: true}, env.adminToken)

	// admin sends a message; u2 (subscribed) should get a state row
	// with read_at = nil.
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "ack me", RequiresAck: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var msg apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &msg))

	// u2 marks read.
	resp, body = env.do(http.MethodPost, "/v1/messages/"+msg.ID+"/read", nil, utok)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var st apiv1.MessageStateResponse
	require.NoError(t, json.Unmarshal(body, &st))
	require.NotNil(t, st.ReadAt)

	// u2 acks reply.
	resp, body = env.do(http.MethodPost, "/v1/messages/"+msg.ID+"/reply-ack", nil, utok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &st))
	require.NotNil(t, st.RepliedAt)
}
