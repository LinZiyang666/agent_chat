package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

func TestPhase3AttachmentAuthzRunsBeforeSizePreflight(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "phase3-attach-authz")
	_, outsiderToken := phase3M7AccountWithToken(t, env, "attach-outsider")
	huge := phase3SparseFile(t, "huge.bin", 26*1024*1024)

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "not a member",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: huge},
			},
		}, outsiderToken)

	require.Equal(t, http.StatusForbidden, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	require.Equal(t, string(errcode.PermDenied), env1.Error.Code)
}

func TestPhase3AttachmentLimitIsPerFileNotAggregate(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "phase3-attach-limit")
	// File sizes adjusted to bracket the NEW 10 MB per-file limit
	// (DiscordAttachmentLimit was lowered from 25 MB to 10 MB after
	// Discord's 2024-09 free-tier cut). Each file 6 MiB < 10 MB so
	// per-file passes; aggregate 12 MiB > 10 MB so this still
	// distinguishes per-file vs aggregate semantics — the original
	// intent of the test.
	first := phase3SparseFile(t, "first.bin", 6*1024*1024)
	second := phase3SparseFile(t, "second.bin", 6*1024*1024)

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "two files",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: first},
				{Path: second},
			},
		}, env.adminToken)

	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var msg apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &msg))
	require.Len(t, msg.Attachments, 2)
}

func phase3M7AccountWithToken(t *testing.T, env *m5Env, name string) (apiv1.AccountResponse, string) {
	t.Helper()
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: name, Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var account apiv1.AccountResponse
	require.NoError(t, json.Unmarshal(body, &account))

	resp, body = env.do(http.MethodPost, "/v1/accounts/"+account.ID+"/tokens", nil, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var tok apiv1.CreateTokenResponse
	require.NoError(t, json.Unmarshal(body, &tok))
	return account, tok.Raw
}

func phase3SparseFile(t *testing.T, pattern string, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), pattern)
	f, err := os.Create(p)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
	return p
}
