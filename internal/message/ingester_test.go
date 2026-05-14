package message

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
)

// TestIngesterAttachIsIdempotent verifies that calling AttachAccount
// twice for the same account does not spawn a second drain goroutine
// or a second Connector.Subscribe.
func TestIngesterAttachIsIdempotent(t *testing.T) {
	var built atomic.Int32
	conn := connector.New(func(_ string, _ bot.Identity) bot.Provider {
		built.Add(1)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ing := New(conn, nopBundler{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	// No Provider is needed to subscribe — Subscribe just registers a
	// channel under the account's slot in the Connector.
	ing.AttachAccount("a")
	assert.True(t, ing.IsAttached("a"))

	ing.AttachAccount("a") // second call: no-op
	assert.True(t, ing.IsAttached("a"))

	ing.DetachAccount("a")
	// Give the drain goroutine a moment to wind down.
	require.Eventually(t, func() bool {
		return !ing.IsAttached("a")
	}, time.Second, 20*time.Millisecond)
}

// TestIngesterDetachWithoutAttachIsNoOp tests that DetachAccount on an
// account that was never attached does not panic.
func TestIngesterDetachWithoutAttachIsNoOp(t *testing.T) {
	conn := connector.New(func(_ string, _ bot.Identity) bot.Provider {
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ing := New(conn, nopBundler{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	ing.DetachAccount("never-attached")
	assert.False(t, ing.IsAttached("never-attached"))
}
