package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// writeTempFile writes content to a fresh file under t.TempDir() and
// returns its absolute path.
func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, content, 0o600))
	return p
}

func TestSendMessageWithAttachmentPersistsRow(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "att-room")
	attPath := writeTempFile(t, "hello.txt", []byte("hello attachment world"))

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "see attached",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: attPath},
			},
		}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var msg apiv1.MessageResponse
	require.NoError(t, json.Unmarshal(body, &msg))
	require.Len(t, msg.Attachments, 1, "response should hydrate attachment row")
	att := msg.Attachments[0]
	assert.Equal(t, "hello.txt", att.Filename)
	assert.EqualValues(t, len("hello attachment world"), att.Size)
	assert.Equal(t, attPath, att.LocalPath, "send-path attachment local_path = source path")
	require.NotNil(t, att.DownloadedAt, "outbound attachments mark downloaded_at immediately")
}

func TestSendMessageAttachmentTooLargeReturns413(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "att-big")
	// 26 MB > Discord 25 MB cap. Use a sparse-ish allocation; we
	// only stat the size, never read the bytes.
	big := make([]byte, 26*1024*1024)
	p := writeTempFile(t, "huge.bin", big)

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: p},
			},
		}, env.adminToken)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.AttachmentTooLarge), env1.Error.Code)
}

func TestSendMessageAttachmentMissingPathReturns400(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "att-missing")

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "hi",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: "/nonexistent/path/should-not-exist.bin"},
			},
		}, env.adminToken)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.InvalidArgument), env1.Error.Code)
	assert.Contains(t, strings.ToLower(env1.Error.Message), "stat attachment")
}

// M7-P3-002 regression: a single oversize file still fails per-file.
func TestSendMessageSingleOversizeAttachmentFails(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "att-single-big")
	big := writeTempFile(t, "huge.bin", make([]byte, 26*1024*1024))

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: big},
			},
		}, env.adminToken)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.AttachmentTooLarge), env1.Error.Code)
}

// M7-P3-002 regression: a multi-file payload where ONE file is over
// the per-file limit still fails, even if the others are small.
func TestSendMessageMixedOneOversizeAttachmentFails(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "att-mixed-big")
	small := writeTempFile(t, "small.txt", []byte("tiny"))
	big := writeTempFile(t, "huge.bin", make([]byte, 26*1024*1024))

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "mixed",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: small},
				{Path: big},
			},
		}, env.adminToken)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.AttachmentTooLarge), env1.Error.Code)
}

func TestListMessagesHydratesAttachments(t *testing.T) {
	env := newM5Env(t)
	room := env.onlineAdminAndCreateRoom(t, "att-list")
	p := writeTempFile(t, "report.md", []byte("# weekly report"))

	resp, body := env.do(http.MethodPost, "/v1/rooms/"+room.ID+"/messages",
		apiv1.SendMessageRequest{
			Content: "see report",
			Attachments: []apiv1.SendAttachmentRequest{
				{Path: p},
			},
		}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, body = env.do(http.MethodGet, "/v1/rooms/"+room.ID+"/messages?limit=10", nil, env.adminToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var listResp apiv1.MessageListResponse
	require.NoError(t, json.Unmarshal(body, &listResp))
	require.NotEmpty(t, listResp.Messages)

	// Find the message with our content and assert the attachment.
	var found *apiv1.MessageResponse
	for i := range listResp.Messages {
		if listResp.Messages[i].Content == "see report" {
			found = &listResp.Messages[i]
			break
		}
	}
	require.NotNil(t, found, "message we just sent should be in the listing")
	require.Len(t, found.Attachments, 1)
	assert.Equal(t, "report.md", found.Attachments[0].Filename)
}
