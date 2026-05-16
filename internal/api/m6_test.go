package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/bot"
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

// Ack with an unknown announcement id collapses to PERM_DENIED so a
// caller can't enumerate ids via response status (M8-S-P2-009 fix).
// The test was named *Returns404 historically because that was the
// pre-fix behaviour — we keep the test name to make the migration
// easy to find via git blame, but the assertion now matches the
// hardened behaviour.
func TestAckAnnouncementUnknownIDReturns404(t *testing.T) {
	env := newM5Env(t)
	resp, body := env.do(http.MethodPost, "/v1/announcements/019e0000-0000-0000-0000-000000000000/read", nil, env.adminToken)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, string(body))
}

// M9 Phase 1: the M6-era TestMentionByBotIDStillSubscribedOnly
// regression guard was removed here. It exercised the content-LIKE
// `<@bot_user_id>` mention path, which M9 retired in favour of the
// `message_mentions` table written by the ingester (and, in M9
// Phase 2, by the send handler after it learns to parse `@<name>`
// from outbound content). The replacement coverage lives at the
// repository layer in
// internal/store/sqlite/sqlite_m5_test.go::TestM5MessageStateReadPathsScopeToSubscribedNonArchivedRooms
// and at the aggregator layer in
// internal/state/aggregator_test.go::TestSnapshotPerUserMentionRequiresSubscribed.
//
// The end-to-end ingester→state path through the API surface is
// covered by TestM9IngesterFanOutsMentionsToState below.

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

// M9 Phase 1: a Discord-sourced message that mentions the viewer's bot
// user id must land in the viewer's snapshot under both the mentions
// feed and the totals.mentions counter, via the new message_mentions
// table (no content-LIKE).
//
// Setup: admin creates room, viewer is subscribed. Then an inbound
// gateway event arrives carrying MentionedBotUserIDs = [viewer's bot
// user id]. The ingester maps bot_user_id → account_id and fans the
// mention out into message_mentions; the state aggregator picks it up.
func TestM9IngesterFanOutsMentionsToState(t *testing.T) {
	env := newM5Env(t)
	room, viewer, viewerTok := roomWithViewer(t, env, "ops", "viewer")

	// mock provider derives bot user id as "u-<account name>" (see
	// the connector factory in newM5Env). Inject a fresh inbound
	// message carrying that snowflake in MentionedBotUserIDs.
	env.latest().InjectMessage(bot.Message{
		ID:                  "discord-mention-1",
		ChannelID:           room.DiscordChannelID,
		AuthorID:            "u-someone-else",
		Content:             "hey can you take a look",
		CreatedAt:           time.Now(),
		MentionedBotUserIDs: []string{"u-" + viewer.Name},
	})

	require.Eventually(t, func() bool {
		resp, body := env.do(http.MethodGet, "/v1/state", nil, viewerTok)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var snap map[string]any
		if err := json.Unmarshal(body, &snap); err != nil {
			return false
		}
		totals, _ := snap["totals"].(map[string]any)
		mentions, _ := totals["mentions"].(float64)
		feed, _ := snap["mentions"].([]any)
		return int(mentions) == 1 && len(feed) == 1
	}, 5*time.Second, 50*time.Millisecond, "viewer should see exactly one mention after ingester runs")
}

// M9 Phase 1: an inbound message flagged @everyone (MentionEveryone)
// must surface in EVERY member's mention feed regardless of their
// subscription — including unsubscribed (旁观) members. This mirrors
// the M6 mention_all semantics through the new mention_everyone column.
func TestM9IngesterFanOutsMentionEveryoneEvenToUnsubscribed(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "broadcast")

	// Create an unsubscribed viewer (旁观 mode).
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "lurker", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var viewer apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &viewer))
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-lurker"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: viewer.ID, Subscribed: false}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+viewer.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var viewerTok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &viewerTok))

	env.latest().InjectMessage(bot.Message{
		ID:              "discord-everyone-1",
		ChannelID:       room.DiscordChannelID,
		AuthorID:        "u-someone-else",
		Content:         "all hands meeting now",
		CreatedAt:       time.Now(),
		MentionEveryone: true,
	})

	require.Eventually(t, func() bool {
		resp, body := env.do(http.MethodGet, "/v1/state", nil, viewerTok.Raw)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var snap map[string]any
		if err := json.Unmarshal(body, &snap); err != nil {
			return false
		}
		totals, _ := snap["totals"].(map[string]any)
		mentions, _ := totals["mentions"].(float64)
		return int(mentions) == 1
	}, 5*time.Second, 50*time.Millisecond, "@everyone must reach unsubscribed (旁观) members")
}
