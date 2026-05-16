package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// M9 Phase 2: SetDiscord verifies bot username via the injected
// IdentityProber. A username mismatch without --force-rename is a
// CONFLICT.
func TestSetDiscordRejectsUsernameMismatch(t *testing.T) {
	env := newM5Env(t)

	// Stage a mismatched identity for the next token the admin
	// sends; mock prober returns it instead of the default
	// "echo hint.Username" behaviour.
	env.prober.SetIdentity("mismatch-token", bot.Identity{
		UserID:   "u-someone-else",
		Username: "someone-else",
	})

	resp, body := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "mismatch-token"}, env.adminToken)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	var envErr apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &envErr))
	assert.Equal(t, string(errcode.Conflict), envErr.Error.Code)
}

// M9 Phase 2: with force_rename=true the daemon asks the prober to
// rename the bot side; mock prober records the call and the next
// Probe returns the new username.
func TestSetDiscordForceRenameInvokesProber(t *testing.T) {
	env := newM5Env(t)
	env.prober.SetIdentity("rename-token", bot.Identity{
		UserID:   "u-stale-name",
		Username: "stale-name",
	})

	resp, body := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "rename-token", ForceRename: true}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	calls := env.prober.RenameCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "rename-token", calls[0].Token)
	// The root account's name is "root" (see newM5Env bootstrap).
	assert.Equal(t, "root", calls[0].NewUsername)
}

// M9 Phase 2: a happy-path set-discord must also persist the probed
// bot_user_id so future room invites / mention resolution can use it
// before the account is brought online. The public AccountResponse
// doesn't surface bot_user_id, so we read straight from the store
// (the only test that needs to peek inside).
func TestSetDiscordPersistsBotUserID(t *testing.T) {
	env := newM5Env(t)
	// Default mock prober echoes Username = account.Name and UserID
	// = "u-" + Username. No override needed.
	resp, _ := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "happy-token"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	stored, err := env.store.Bundle().Accounts.Get(t.Context(), env.adminID)
	require.NoError(t, err)
	assert.Equal(t, "u-root", stored.BotUserID,
		"set-discord should snap the bot_user_id from the probed identity")
}

// M9 Phase 2: ReadRoom must hydrate the four fields the design
// spec'd: author_name, display_content, read_at, current_announcement_id.
func TestReadRoomHydratesPhase2Fields(t *testing.T) {
	env := newM5Env(t)
	room, viewer, viewerTok := roomWithViewer(t, env, "ops-readroom", "viewer-readroom")

	// admin sends a message containing a <@bot_user_id> form to
	// exercise display_content rewriting. The "viewer-readroom"
	// account has bot_user_id "u-viewer-readroom" (mock provider).
	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "hello @viewer-readroom"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var sent apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &sent))

	// Admin also posts an announcement so we can check the
	// current_announcement_id field.
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "stand-up at 0900"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var ann apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &ann))

	// Viewer reads the room.
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/read",
		apiv1.ReadRoomRequest{}, viewerTok)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var read apiv1.ReadRoomResponse
	require.NoError(t, json.Unmarshal(body, &read))

	require.NotEmpty(t, read.Messages)
	assert.Equal(t, ann.ID, read.Room.CurrentAnnouncementID,
		"current_announcement_id must point at the latest version")

	// Find the message we sent and assert all four hydrated fields.
	var found *apiv1.MessageResponse
	for i := range read.Messages {
		if read.Messages[i].ID == sent.ID {
			found = &read.Messages[i]
			break
		}
	}
	require.NotNil(t, found, "the sent message must appear in the read response")
	assert.Equal(t, "root", found.AuthorName,
		"author_name must resolve to the admin's account name")
	assert.Equal(t, "hello @viewer-readroom", found.DisplayContent,
		"display_content must rewrite <@id> to @<name>")
	require.NotNil(t, found.ReadAt, "viewer just read the room, read_at must be set")
	assert.False(t, found.ReadAt.IsZero())
	require.Contains(t, found.Mentions, viewer.ID)
}

// M9 Phase 2 P2-a review fix: room.current_announcement_id must be
// populated even when the room currently has no readable messages.
// The earlier hydration block short-circuited on len(ids)==0 and
// silently returned an empty CurrentAnnouncementID — broken for a
// freshly-created room that already has an announcement, or a viewer
// who has read everything.
func TestReadRoomCurrentAnnouncementIDPresentWhenEmpty(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "empty-with-ann")

	// Post an announcement BEFORE any messages get sent into the
	// room. ReadRoom is then called on a room whose message list
	// is empty for the actor.
	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/announcement",
		apiv1.CreateAnnouncementRequest{Content: "weekly sync moved"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var ann apiv1.AnnouncementResponse
	require.NoError(t, json.Unmarshal(body, &ann))

	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/read",
		apiv1.ReadRoomRequest{}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var read apiv1.ReadRoomResponse
	require.NoError(t, json.Unmarshal(body, &read))
	assert.Empty(t, read.Messages,
		"the room has no messages yet — the empty-feed branch is what we want to exercise")
	assert.Equal(t, ann.ID, read.Room.CurrentAnnouncementID,
		"current_announcement_id must hydrate even when the message slice is empty")
}

// M9 Phase 2 P3 review fix: --force-rename via the ?force_rename=true
// query string must work, not just via the body field.
func TestSetDiscordForceRenameViaQueryString(t *testing.T) {
	env := newM5Env(t)
	env.prober.SetIdentity("query-rename-token", bot.Identity{
		UserID:   "u-stale",
		Username: "stale",
	})

	resp, body := env.do(http.MethodPost,
		"/v1/accounts/"+env.adminID+"/discord?force_rename=true",
		// Body intentionally OMITS ForceRename so we exercise the
		// query-string contract on its own.
		apiv1.SetDiscordRequest{BotToken: "query-rename-token"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	calls := env.prober.RenameCalls()
	require.Len(t, calls, 1, "the query-string flag must reach the prober")
	assert.Equal(t, "root", calls[0].NewUsername)
}

// Invalid `?force_rename=` values must surface as INVALID_ARGUMENT
// rather than silently being treated as false.
func TestSetDiscordForceRenameQueryRejectsBogusValue(t *testing.T) {
	env := newM5Env(t)
	resp, body := env.do(http.MethodPost,
		"/v1/accounts/"+env.adminID+"/discord?force_rename=banana",
		apiv1.SetDiscordRequest{BotToken: "any"}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	var envErr apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &envErr))
	assert.Equal(t, string(errcode.InvalidArgument), envErr.Error.Code)
}

// M9 Phase 2: --before defaults to 50 (not 10) per design spec §3.3.
func TestReadRoomBeforeDefaultsTo50(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "paging")

	// Drop 55 messages so we can verify the default page returns
	// exactly 50 (the new limit) when paginating from the newest.
	for i := 0; i < 55; i++ {
		resp, _ := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
			apiv1.SendMessageRequest{Content: "msg"}, env.adminToken)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
	}

	// Read the room once to grab a cursor id; then --before that id
	// with no explicit limit should return up to 50 older messages.
	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/read",
		apiv1.ReadRoomRequest{Limit: 200}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var initial apiv1.ReadRoomResponse
	require.NoError(t, json.Unmarshal(body, &initial))
	require.NotEmpty(t, initial.Messages)
	newestID := initial.Messages[len(initial.Messages)-1].ID

	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/read",
		apiv1.ReadRoomRequest{Before: newestID}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var before apiv1.ReadRoomResponse
	require.NoError(t, json.Unmarshal(body, &before))
	assert.Len(t, before.Messages, 50,
		"--before with no --limit must default to 50, not 10")
	assert.Empty(t, before.MarkedRead,
		"--before is pure-query; nothing should be marked read")
}
