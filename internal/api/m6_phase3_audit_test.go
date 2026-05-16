package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
)

func TestPhase3MentionAllReachesUnsubscribedCurrentMember(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "phase3-all")
	viewer, viewerToken := phase3OnlineAccountWithToken(t, env, "observer")
	phase3Invite(t, env, room.ID, viewer.ID, false)

	// M9 Phase 2: legacy MentionAll flag retired. The replacement is
	// the @everyone literal in content — the outbound parser flags it
	// and the aggregator counts mention_everyone for every member.
	var resp *http.Response
	var body []byte
	var sent apiv1.MessageResponse
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages", apiv1.SendMessageRequest{
		Content: "@everyone standup now",
	}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	require.NoError(t, json.Unmarshal(body, &sent))

	resp, body = env.do(http.MethodGet, "/v1/state", nil, viewerToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))

	totals := snap["totals"].(map[string]any)
	mentions, _ := snap["mentions"].([]any)
	if totals["mentions"] != float64(1) || len(mentions) != 1 {
		t.Fatalf("mention_all should notify every current room member, including unsubscribed members; totals=%v len=%d", totals["mentions"], len(mentions))
	}
	got := mentions[0].(map[string]any)
	if got["id"] != sent.ID {
		t.Fatalf("mention id = %q, want %q", got["id"], sent.ID)
	}
}

// M9 Phase 2: raw <@id> in outbound content is rejected by the
// parser. The M6-era TestPhase3BotIDMentionDoesNotReachUnsubscribedMember
// asserted the inverse ("if you do write <@id>, only subscribed
// members get the mention") — that whole branch is gone, so the test
// becomes a stricter "send is rejected outright" check.
func TestPhase3RawBotIDMentionRejected(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "phase3-bot-mention")
	viewer, _ := phase3OnlineAccountWithToken(t, env, "botobserver")
	phase3Invite(t, env, room.ID, viewer.ID, false)

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages", apiv1.SendMessageRequest{
		Content: "hello <@u-botobserver>",
	}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
}

func TestPhase3MentionAllDoesNotReachFutureMember(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "phase3-future-all")

	// M9 Phase 2: @everyone in content replaces the M6 MentionAll flag.
	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages", apiv1.SendMessageRequest{
		Content: "@everyone before you joined",
	}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	viewer, viewerToken := phase3OnlineAccountWithToken(t, env, "future")
	phase3Invite(t, env, room.ID, viewer.ID, true)

	resp, body = env.do(http.MethodGet, "/v1/state", nil, viewerToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var snap map[string]any
	require.NoError(t, json.Unmarshal(body, &snap))

	totals := snap["totals"].(map[string]any)
	mentions, _ := snap["mentions"].([]any)
	if totals["mentions"] != float64(0) || len(mentions) != 0 {
		t.Fatalf("historical mention_all should not notify future members; totals=%v len=%d", totals["mentions"], len(mentions))
	}
}

func phase3OnlineAccountWithToken(t *testing.T, env *m5Env, name string) (apiv1.AccountResponse, string) {
	t.Helper()
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: name, Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var account apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &account))

	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+account.ID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-" + name}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = env.do(http.MethodPost, "/v1/accounts/"+account.ID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+account.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))
	return account, tok.Raw
}

func phase3Invite(t *testing.T, env *m5Env, roomID, accountID string, subscribed bool) {
	t.Helper()
	resp, _ := env.do(http.MethodPost, "/v1/rooms/"+roomID+"/members", apiv1.InviteRequest{
		AccountID:  accountID,
		Subscribed: subscribed,
	}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
}
