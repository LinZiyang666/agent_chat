package client

import (
	"context"
	"crypto/rand"
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
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

// startM4TestDaemon stands up the M4 daemon stack: same as the M3
// rig but with the message Ingester wired into Deps so room/message
// routes are fully functional. Returns socket path, admin token,
// and the most recent mock Provider getter.
func startM4TestDaemon(t *testing.T) (sock, token string, latest func() *mock.Provider) {
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

	// Promote root to admin (CreateRoom is admin-only). Bootstrap
	// creates it with admin role already, but be explicit so the test
	// is self-documenting if defaults ever change.
	rootAcc, err := bundle.Accounts.Get(context.Background(), root.ID)
	require.NoError(t, err)
	require.Equal(t, "admin", string(rootAcc.Role))

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

	ing := message.New(conn, s, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

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

// bringRootOnline does set-discord + online on the admin token's
// owning account so room CRUD works.
func bringRootOnline(t *testing.T, c *Client) {
	t.Helper()
	me, err := c.Whoami(context.Background())
	require.NoError(t, err)
	_, err = c.SetDiscord(context.Background(), me.Account.ID, "fake-root")
	require.NoError(t, err)
	_, err = c.Online(context.Background(), me.Account.ID)
	require.NoError(t, err)
}

func TestClientCreateAndListRooms(t *testing.T) {
	sock, tok, _ := startM4TestDaemon(t)
	c := New(sock, tok)
	bringRootOnline(t, c)

	r, err := c.CreateRoom(context.Background(), "ops")
	require.NoError(t, err)
	assert.Equal(t, "ops", r.Name)
	assert.Equal(t, "ch-ops", r.DiscordChannelID)
	assert.False(t, r.Archived)

	rooms, err := c.ListRooms(context.Background(), ListRoomsOptions{})
	require.NoError(t, err)
	require.Len(t, rooms, 1)
	assert.Equal(t, r.ID, rooms[0].ID)

	got, err := c.GetRoom(context.Background(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, r.ID, got.ID)
}

func TestClientRenameAndArchiveRoom(t *testing.T) {
	sock, tok, _ := startM4TestDaemon(t)
	c := New(sock, tok)
	bringRootOnline(t, c)

	r, err := c.CreateRoom(context.Background(), "first")
	require.NoError(t, err)

	r2, err := c.RenameRoom(context.Background(), r.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "second", r2.Name)

	arch, err := c.ArchiveRoom(context.Background(), r.ID)
	require.NoError(t, err)
	assert.True(t, arch.Archived)

	// Default list excludes archived.
	rooms, err := c.ListRooms(context.Background(), ListRoomsOptions{})
	require.NoError(t, err)
	assert.Len(t, rooms, 0)

	rooms, err = c.ListRooms(context.Background(), ListRoomsOptions{IncludeArchived: true})
	require.NoError(t, err)
	require.Len(t, rooms, 1)
}

func TestClientInviteAndKickMember(t *testing.T) {
	sock, tok, latest := startM4TestDaemon(t)
	c := New(sock, tok)
	bringRootOnline(t, c)
	adminProvider := latest()

	r, err := c.CreateRoom(context.Background(), "team")
	require.NoError(t, err)

	// Create + online a target so it captures bot_user_id.
	target, err := c.CreateAccount(context.Background(), "alice", "user")
	require.NoError(t, err)
	_, err = c.SetDiscord(context.Background(), target.ID, "fake-alice")
	require.NoError(t, err)
	_, err = c.Online(context.Background(), target.ID)
	require.NoError(t, err)

	m, err := c.InviteMember(context.Background(), r.ID, target.ID, true)
	require.NoError(t, err)
	assert.Equal(t, target.ID, m.AccountID)
	assert.True(t, m.Subscribed)
	assert.Contains(t, adminProvider.Added, [2]string{"ch-team", "u-alice"})

	members, err := c.ListMembers(context.Background(), r.ID)
	require.NoError(t, err)
	require.Len(t, members, 2) // root (creator) + alice

	require.NoError(t, c.KickMember(context.Background(), r.ID, target.ID))
	assert.Contains(t, adminProvider.Removed, [2]string{"ch-team", "u-alice"})

	members, err = c.ListMembers(context.Background(), r.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)
}

func TestClientSetSubscribed(t *testing.T) {
	sock, tok, _ := startM4TestDaemon(t)
	admin := New(sock, tok)
	bringRootOnline(t, admin)
	r, err := admin.CreateRoom(context.Background(), "x")
	require.NoError(t, err)
	target, err := admin.CreateAccount(context.Background(), "u", "user")
	require.NoError(t, err)
	_, err = admin.SetDiscord(context.Background(), target.ID, "fake-u")
	require.NoError(t, err)
	_, err = admin.Online(context.Background(), target.ID)
	require.NoError(t, err)
	_, err = admin.InviteMember(context.Background(), r.ID, target.ID, false)
	require.NoError(t, err)

	// Issue a token for the target and have them self-subscribe.
	rawTok, err := admin.CreateToken(context.Background(), target.ID)
	require.NoError(t, err)
	user := New(sock, rawTok.Raw)

	m, err := user.SetSubscribed(context.Background(), r.ID, true)
	require.NoError(t, err)
	assert.True(t, m.Subscribed)

	m, err = user.SetSubscribed(context.Background(), r.ID, false)
	require.NoError(t, err)
	assert.False(t, m.Subscribed)
}

// M9 Phase 2: TestClientSendAndListMessages was rewritten around the
// new ReadRoom verb. The old List endpoint (GET /rooms/{id}/messages)
// was retired; agents now use POST /rooms/{id}/read which both lists
// and marks-as-read in one shot, with --before for pure-query paging.
func TestClientSendAndReadRoom(t *testing.T) {
	sock, tok, _ := startM4TestDaemon(t)
	c := New(sock, tok)
	bringRootOnline(t, c)
	r, err := c.CreateRoom(context.Background(), "general")
	require.NoError(t, err)

	sent, err := c.SendMessage(context.Background(), r.ID, "first message", SendMessageOptions{})
	require.NoError(t, err)
	assert.Equal(t, "first message", sent.Content)
	assert.NotEmpty(t, sent.DiscordMsgID)

	// Send with options.
	withOpts, err := c.SendMessage(context.Background(), r.ID, "second", SendMessageOptions{
		Priority: "urgent",
	})
	require.NoError(t, err)
	assert.Equal(t, "urgent", withOpts.Priority)

	// The send path marks the author's own state as read, so root
	// has no unread on this room. Read default mode therefore
	// returns 0 marked_read but does surface both messages as
	// context.
	resp, err := c.ReadRoom(context.Background(), r.ID, ReadRoomOptions{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.MarkedRead, "author already counts as read")
	require.Len(t, resp.Messages, 2)
	// ReadRoom returns oldest -> newest.
	assert.Equal(t, sent.ID, resp.Messages[0].ID)
	assert.Equal(t, withOpts.ID, resp.Messages[1].ID)

	// --before paginates without touching read state.
	page, err := c.ReadRoom(context.Background(), r.ID, ReadRoomOptions{
		Before: withOpts.ID, Limit: 5,
	})
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	assert.Equal(t, sent.ID, page.Messages[0].ID)
	assert.Empty(t, page.MarkedRead, "--before mode must NOT mark anything")
}

// M9 Phase 2 supersedes the M4-era TestClientMarkReadAndReplyAck:
// MarkMessageRead and ReplyAckMessage are gone. The replacement
// coverage — "an agent reads a room and unread rows flip to read" —
// lives in TestM9ReadRoomMarksUnreadOnce in internal/api.
func TestClientReadRoomMarksUnread(t *testing.T) {
	sock, tok, _ := startM4TestDaemon(t)
	admin := New(sock, tok)
	bringRootOnline(t, admin)
	r, err := admin.CreateRoom(context.Background(), "ack-room")
	require.NoError(t, err)

	target, err := admin.CreateAccount(context.Background(), "ack-user", "user")
	require.NoError(t, err)
	_, err = admin.SetDiscord(context.Background(), target.ID, "fake-acku")
	require.NoError(t, err)
	_, err = admin.Online(context.Background(), target.ID)
	require.NoError(t, err)
	_, err = admin.InviteMember(context.Background(), r.ID, target.ID, true)
	require.NoError(t, err)

	msg, err := admin.SendMessage(context.Background(), r.ID, "answer me",
		SendMessageOptions{})
	require.NoError(t, err)

	rawTok, err := admin.CreateToken(context.Background(), target.ID)
	require.NoError(t, err)
	user := New(sock, rawTok.Raw)

	// First read: the message is unread for the user -> ends up in
	// marked_read.
	resp, err := user.ReadRoom(context.Background(), r.ID, ReadRoomOptions{})
	require.NoError(t, err)
	require.Contains(t, resp.MarkedRead, msg.ID)

	// Second read: already read, so marked_read is empty.
	again, err := user.ReadRoom(context.Background(), r.ID, ReadRoomOptions{})
	require.NoError(t, err)
	assert.Empty(t, again.MarkedRead)
}

func TestClientDeleteRoom(t *testing.T) {
	sock, tok, _ := startM4TestDaemon(t)
	c := New(sock, tok)
	bringRootOnline(t, c)
	r, err := c.CreateRoom(context.Background(), "throwaway")
	require.NoError(t, err)

	require.NoError(t, c.DeleteRoom(context.Background(), r.ID))

	rooms, err := c.ListRooms(context.Background(), ListRoomsOptions{IncludeArchived: true})
	require.NoError(t, err)
	assert.Len(t, rooms, 0)
}
