package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

func onlineRootAndCreateRoom(t *testing.T, env *m4Env, name string) apiv1.RoomResponse {
	t.Helper()
	resp, body := env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/discord",
		apiv1.SetDiscordRequest{BotToken: "fake-root"}, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+env.adminID+"/online", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	resp, body = env.do(http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: name}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var room apiv1.RoomResponse
	require.NoError(t, json.Unmarshal(body, &room))
	return room
}

func TestPhase3AdminCanSendWithoutMembership(t *testing.T) {
	env := newM4Env(t)
	room := onlineRootAndCreateRoom(t, env, "ops")

	admin2 := env.onlineAccount(t, "admin2", "admin")
	admin2Token := env.issueToken(t, admin2.ID)

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "admin override"}, admin2Token)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
}

func TestPhase3DeleteRoomPropagatesDiscordFailure(t *testing.T) {
	env := newM4Env(t)
	room := onlineRootAndCreateRoom(t, env, "delete-failure")
	adminProvider := env.latest()
	adminProvider.DeleteChannelErr = errcode.New(errcode.Unavailable, "discord delete failed")

	resp, body := env.do(http.MethodDelete, "/v1/rooms/"+room.ID, nil, env.adminToken)
	require.NotEqual(t, http.StatusNoContent, resp.StatusCode, string(body))

	got, err := env.store.Bundle().Rooms.Get(context.Background(), room.ID)
	require.NoError(t, err)
	assert.Equal(t, room.ID, got.ID)
}

func TestPhase3KickMemberPropagatesDiscordFailure(t *testing.T) {
	env := newM4Env(t)
	room := onlineRootAndCreateRoom(t, env, "kick-failure")
	adminProvider := env.latest()
	target := env.onlineAccount(t, "alice", "user")

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: target.ID, Subscribed: true}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	adminProvider.RemoveMemberErr = errcode.New(errcode.Unavailable, "discord remove failed")
	resp, body = env.do(http.MethodDelete, "/v1/rooms/"+room.ID+"/members/"+target.ID, nil, env.adminToken)
	require.NotEqual(t, http.StatusNoContent, resp.StatusCode, string(body))

	_, err := env.store.Bundle().Memberships.Get(context.Background(), target.ID, room.ID)
	require.NoError(t, err)
}

func TestPhase3InviteFailurePreservesExistingMembership(t *testing.T) {
	env := newM4Env(t)
	room := onlineRootAndCreateRoom(t, env, "invite-failure")
	adminProvider := env.latest()
	target := env.onlineAccount(t, "bob", "user")

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: target.ID, Subscribed: false}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	adminProvider.AddMemberErr = errors.New("discord grant failed")
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: target.ID, Subscribed: true}, env.adminToken)
	require.NotEqual(t, http.StatusCreated, resp.StatusCode, string(body))

	got, err := env.store.Bundle().Memberships.Get(context.Background(), target.ID, room.ID)
	require.NoError(t, err)
	assert.False(t, got.Subscribed, "failed re-invite must preserve the prior subscribed=false membership")
}

func TestPhase3UnsubscribedMemberGetsMessageState(t *testing.T) {
	env := newM4Env(t)
	room := onlineRootAndCreateRoom(t, env, "secondary-state")
	target := env.onlineAccount(t, "observer", "user")

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/members",
		apiv1.InviteRequest{AccountID: target.ID, Subscribed: false}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "visible to observers"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var msg apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &msg))

	st, err := env.store.Bundle().MessageStates.Get(context.Background(), msg.ID, target.ID)
	require.NoError(t, err)
	assert.Nil(t, st.ReadAt)
}

func TestPhase3SendRacePreservesLocalMetadata(t *testing.T) {
	env := newM4Env(t)
	room := onlineRootAndCreateRoom(t, env, "race-metadata")

	// Simulate the ingester winning the INSERT race for the Discord id
	// that the mock provider will return on the next SendMessage call.
	// The ingester only knows Discord-native fields, so the send path
	// must still persist agentchat-local metadata from the request.
	preexisting := &store.Message{
		ID:              "race-existing-message",
		RoomID:          room.ID,
		AuthorAccountID: env.adminID,
		DiscordMsgID:    "msg-1",
		Content:         "needs metadata",
		RequiresAck:     false,
		Priority:        store.PriorityNormal,
		CreatedAt:       time.Now().UTC(),
		ContentHash:     "hash-from-ingester",
	}
	require.NoError(t, env.store.Bundle().Messages.Create(context.Background(), preexisting))

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content:     "needs metadata",
			RequiresAck: true,
			Priority:    string(store.PriorityUrgent),
		}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	var msg apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &msg))
	assert.Equal(t, preexisting.ID, msg.ID)
	assert.True(t, msg.RequiresAck, "send-path metadata must survive the ingest-wins race")
	assert.Equal(t, string(store.PriorityUrgent), msg.Priority)

	stored, err := env.store.Bundle().Messages.Get(context.Background(), preexisting.ID)
	require.NoError(t, err)
	assert.True(t, stored.RequiresAck)
	assert.Equal(t, store.PriorityUrgent, stored.Priority)
}
