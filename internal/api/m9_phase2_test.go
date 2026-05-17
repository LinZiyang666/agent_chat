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

// Discord is the authority on bot identity: when Discord-side bot
// username differs from the local account.name, set-discord snaps the
// local row to Discord's name (no --force-rename, no CONFLICT). The
// rename pathway is replaced by "trust Discord and adopt".
func TestSetDiscordSnapsLocalNameToBotUsername(t *testing.T) {
	env := newM5Env(t)
	env.prober.SetIdentity("mismatch-token", bot.Identity{
		UserID:   "u-discord-side",
		Username: "discord-side",
	})

	resp, body := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "mismatch-token"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var got apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "discord-side", got.Name,
		"local account.name must be snapped to Discord's bot username")

	// Daemon must NOT have asked the prober to rename Discord — the
	// new direction is one-way (Discord -> local).
	assert.Empty(t, env.prober.RenameCalls(),
		"set-discord must not call prober.Rename in the new direction")

	stored, err := env.store.Bundle().Accounts.Get(t.Context(), env.adminID)
	require.NoError(t, err)
	assert.Equal(t, "discord-side", stored.Name)
	assert.Equal(t, "u-discord-side", stored.BotUserID)
}

// When the Discord-reported bot username collides with another
// agentchat account, the local-name sync hits the unique-name guard
// and returns CONFLICT (no partial write).
func TestSetDiscordSnappedNameCollisionConflicts(t *testing.T) {
	env := newM5Env(t)

	// Make a second account named "taken" so adopting it would clash.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "taken", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	env.prober.SetIdentity("collision-token", bot.Identity{
		UserID:   "u-collision",
		Username: "taken",
	})

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "collision-token"}, env.adminToken)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	var envErr apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &envErr))
	assert.Equal(t, string(errcode.Conflict), envErr.Error.Code)
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

// account rename (PATCH /accounts/{id}) pushes the new name to
// Discord via prober.Rename before updating the local row, when the
// account has a bot token. Order matters: a Discord rate-limit
// failure must leave the local row unchanged.
func TestUpdateAccountRenamePushesToDiscord(t *testing.T) {
	env := newM5Env(t)

	// Adopt a bot first so the account has a token to drive the
	// rename PATCH from.
	resp, _ := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "rename-prep-token"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Drain any prior rename activity so we only see the rename
	// triggered by the PATCH itself. (RenameCalls() reads-and-resets.)
	_ = env.prober.RenameCalls()

	newName := "renamed"
	resp, body := env.do(http.MethodPatch, "/v1/accounts/"+env.adminID,
		apiv1.UpdateAccountRequest{Name: &newName}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var got apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "renamed", got.Name)

	calls := env.prober.RenameCalls()
	require.Len(t, calls, 1, "PATCH must call Discord rename once")
	assert.Equal(t, "renamed", calls[0].NewUsername)
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
