package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
)

// m6Env reuses m5Env (state bus + ingester wiring) — announcements and
// the @all flag plumb through the same code paths.

// roomWithViewer brings a fresh "viewer" online, subscribes them to a
// freshly created room, and returns (room, viewer-account, viewer-token).
// The admin is already online from onlineAdminAndCreateRoom.
func roomWithViewer(t *testing.T, env *m5Env, roomName, viewerName string) (apiv1.RoomResponse, apiv1.AccountResponse, string) {
	t.Helper()
	room := env.onlineAdminAndCreateRoom(t, roomName)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: viewerName, Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var viewer apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &viewer))

	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-v"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: viewer.ID, Subscribed: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))
	return room, viewer, tok.Raw
}

func TestAnnouncementCreateBumpsVersionAndUnreadsAll(t *testing.T) {
	env := newM5Env(t)
	room, _, viewerTok := roomWithViewer(t, env, "ops", "viewer")

	// First announcement.
	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "v2 protocol enabled"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var ann1 apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &ann1))
	assert.Equal(t, 1, ann1.Version)

	// Second announcement bumps version.
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "addendum: maintenance window"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var ann2 apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &ann2))
	assert.Equal(t, 2, ann2.Version)

	// Viewer's state should show one unread announcement (only the
	// latest version counts).
	resp, body = env.do(http.MethodGet, "/v1/state", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	totals := snap["totals"].(map[string]any)
	assert.EqualValues(t, 1, totals["announcements"], "viewer sees only latest announcement as unread")
	anns, _ := snap["announcements"].([]any)
	require.Len(t, anns, 1)
	a0 := anns[0].(map[string]any)
	assert.Equal(t, ann2.ID, a0["announcement_id"])
	assert.EqualValues(t, 2, a0["version"])
}

func TestAnnouncementGetLatestReflectsReadFlag(t *testing.T) {
	env := newM5Env(t)
	room, _, viewerTok := roomWithViewer(t, env, "ops2", "viewer2")

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "important"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var ann apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &ann))

	// Viewer fetches: read=false.
	resp, body = env.do(http.MethodGet, "/v1/rooms/"+room.ID+"/announcement", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var fetched apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &fetched))
	assert.False(t, fetched.Read)

	// Viewer acks; refetch: read=true.
	resp, _ = env.do(http.MethodPost, "/v1/announcements/"+ann.ID+"/read", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, body = env.do(http.MethodGet, "/v1/rooms/"+room.ID+"/announcement", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &fetched))
	assert.True(t, fetched.Read)

	// Viewer's announcement-totals goes back to 0.
	resp, body = env.do(http.MethodGet, "/v1/state", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	totals := snap["totals"].(map[string]any)
	assert.EqualValues(t, 0, totals["announcements"])
}

func TestSystemAnnouncementUnreadAndAck(t *testing.T) {
	env := newM5Env(t)
	// Admin posts a system announcement.
	resp, body := env.do(http.MethodPost, "/v1/system/announcements",
		apiv1.CreateSystemAnnouncementRequest{Content: "scheduled maintenance Sunday 2 AM"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var sys apiv1.SystemAnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &sys))

	// Fresh viewer (no membership needed for system announcements).
	resp, body = env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "vsys", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var viewer apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &viewer))
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))

	// Viewer sees it unread in state + in list.
	resp, body = env.do(http.MethodGet, "/v1/state", nil, tok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	totals := snap["totals"].(map[string]any)
	assert.EqualValues(t, 1, totals["system_announcements"])

	resp, body = env.do(http.MethodGet, "/v1/system/announcements", nil, tok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var listResp apiv1.SystemAnnouncementListResponse
	require.NoError(t, json.Unmarshal(body, &listResp))
	require.Len(t, listResp.Announcements, 1)
	assert.False(t, listResp.Announcements[0].Read)

	// ACK.
	resp, _ = env.do(http.MethodPost, "/v1/system/announcements/"+sys.ID+"/read", nil, tok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// State zeros out, list now shows read=true.
	resp, body = env.do(http.MethodGet, "/v1/state", nil, tok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &snap))
	totals = snap["totals"].(map[string]any)
	assert.EqualValues(t, 0, totals["system_announcements"])

	resp, body = env.do(http.MethodGet, "/v1/system/announcements", nil, tok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &listResp))
	require.Len(t, listResp.Announcements, 1)
	assert.True(t, listResp.Announcements[0].Read)
}

func TestSystemAnnouncementCreateForbiddenForNonAdmin(t *testing.T) {
	env := newM5Env(t)
	// Make a user-role account and try to post a system announcement.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "alice", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var alice apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &alice))
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+alice.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))

	resp, _ = env.do(http.MethodPost, "/v1/system/announcements",
		apiv1.CreateSystemAnnouncementRequest{Content: "should not work"}, tok.Raw)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestSendMessageWithMentionAllSurfacesInMentions(t *testing.T) {
	env := newM5Env(t)
	room, _, viewerTok := roomWithViewer(t, env, "broadcast", "viewer3")

	// Admin sends with mention_all=true. Note: the message content
	// does NOT contain the viewer's literal <@bot_user_id>; only the
	// mention_all flag should drive its inclusion in the mentions
	// feed.
	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "drill at 0600", MentionAll: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var msg apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &msg))
	assert.True(t, msg.MentionAll)

	resp, body = env.do(http.MethodGet, "/v1/state", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	totals := snap["totals"].(map[string]any)
	assert.EqualValues(t, 1, totals["mentions"], "mention_all forces every member into mentions feed")
	mentions, _ := snap["mentions"].([]any)
	require.Len(t, mentions, 1)
	m0 := mentions[0].(map[string]any)
	assert.Equal(t, "drill at 0600", m0["content"])
}

// M6-S6 covered: ack is idempotent (read_at is overwritten on re-ack).
func TestAckAnnouncementIdempotent(t *testing.T) {
	env := newM5Env(t)
	room, _, viewerTok := roomWithViewer(t, env, "idem", "videm")

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "double-ack me"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var ann apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &ann))

	resp, body = env.do(http.MethodPost, "/v1/announcements/"+ann.ID+"/read", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var first apiv1.AnnouncementReadResponse
	require.NoError(t, json.Unmarshal(body, &first))

	// Second ack must succeed without error.
	resp, body = env.do(http.MethodPost, "/v1/announcements/"+ann.ID+"/read", nil, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var second apiv1.AnnouncementReadResponse
	require.NoError(t, json.Unmarshal(body, &second))
	assert.Equal(t, first.AnnouncementID, second.AnnouncementID)
	assert.Equal(t, first.AccountID, second.AccountID)
}

// M6-S6 covered: GET a room with no announcements returns 404 (the
// repo path returns errcode.NotFound for an empty room).
func TestGetAnnouncementNotFoundForEmptyRoom(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "empty")
	resp, body := env.do(http.MethodGet, "/v1/rooms/"+room.ID+"/announcement", nil, env.adminToken)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))
}

// M6-S6 covered: ack with an unknown announcement id is rejected with 404.
func TestAckAnnouncementUnknownIDReturns404(t *testing.T) {
	env := newM5Env(t)
	resp, body := env.do(http.MethodPost, "/v1/announcements/019e0000-0000-0000-0000-000000000000/read", nil, env.adminToken)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))
}

// M6-P3-001 regression guard: subscribed members with a literal
// <@bot> content match keep the M5 subscribed-only semantics; the
// mention_all widening must not break the bot-id path.
func TestMentionByBotIDStillSubscribedOnly(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "p3guard")

	// Subscribed viewer with a real bot identity (so <@user_id> can match).
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "sub", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var sub apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &sub))
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+sub.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-sub"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+sub.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The mock Provider's Identity assigns user_id = "u-<username>".
	resp, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: sub.ID, Subscribed: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+sub.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var subTok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &subTok))

	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "hello <@u-sub>"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	resp, body = env.do(http.MethodGet, "/v1/state", nil, subTok.Raw)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))
	assert.EqualValues(t, 1, snap["totals"].(map[string]any)["mentions"],
		"subscribed member with literal <@bot_user_id> match must still surface")
}

func TestAnnouncementCreateRejectedFromOutsider(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "private")
	// Outsider has no membership.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "outsider", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var out apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &out))
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+out.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))

	resp, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "leak"}, tok.Raw)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
