package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// M8 S-P1-001 regression: any mutating endpoint must reject a JSON
// body larger than apiv1.MaxRequestBodyBytes with HTTP 413 /
// PAYLOAD_TOO_LARGE so a malicious low-priv caller cannot OOM the
// daemon by streaming a multi-GB body. We exercise /v1/accounts —
// the smallest admin-only DecodeJSON path — but every handler that
// calls DecodeJSON is now protected by the same wrapper.
func TestDecodeJSONRejectsOversizeBody(t *testing.T) {
	env := newM5Env(t)

	// Build a Name field that, once JSON-encoded, comfortably exceeds
	// the 1 MiB cap. 2 MiB of 'a's is more than double the limit.
	huge := strings.Repeat("a", 2*1024*1024)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: huge, Role: "user"}, env.adminToken)

	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, string(body))
	var env1 apiv1.ErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &env1))
	assert.Equal(t, string(errcode.PayloadTooLarge), env1.Error.Code)
}

// Sanity: a small body still succeeds (we didn't break the happy path).
func TestDecodeJSONAcceptsNormalBody(t *testing.T) {
	env := newM5Env(t)
	resp, body := env.do(http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: "small-name", Role: "user"}, env.adminToken)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
}
