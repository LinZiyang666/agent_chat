package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// M8-S-P2-004 regression: SendMessage must Lstat (not Stat) the
// attachment path and refuse to follow a symlink so an attacker
// with write access to the parent directory cannot swap the file
// between our stat and bot.Provider's open.
func TestPhase3M8AttachmentRejectsSymlink(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "m8-symlink")

	// Two files: the real target (small) and a symlink pointing at it.
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(real, []byte("payload"), 0o600))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(real, link))

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "symlink test",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: link},
			},
		}, env.adminToken)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.InvalidArgument), env1.Error.Code)
	assert.Contains(t, env1.Error.Message, "symlink")
}

// M8-S-P2-012 regression: a non-admin token cannot send a message
// with priority=system. The "system" priority surfaces in the state
// UI's priority feed as operator/announcement traffic — letting a
// user impersonate it would be a noise vector.
func TestPhase3M8PriorityForbidsSystemForUser(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "m8-priority")

	// Create a non-admin user account.
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "u1", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var user apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &user))

	// Issue a token for that user.
	resp, body = env.do(http.MethodPost, "/v1/accounts/"+user.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var tk apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tk))
	require.NotEmpty(t, tk.Raw)

	// User attempts priority=system. The priority gate is treated as
	// an argument-shape violation: the "system" value of the priority
	// enum is admin-only, so non-admin callers get INVALID_ARGUMENT
	// (matches the USAGE error-code matrix; exit code 22).
	resp, body = env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{Content: "pretend system", Priority: "system"}, tk.Raw)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.InvalidArgument), env1.Error.Code)
}

// M8-S-P1-001 regression already lives in TestDecodeJSONRejectsOversizeBody;
// the constant cap and 413 contract are covered there.
