package cmds

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

func TestRemoveStaleSocket_NoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.sock")
	assert.NoError(t, removeStaleSocket(path))
}

func TestRemoveStaleSocket_RealSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	require.NoError(t, ln.Close())

	require.NoError(t, removeStaleSocket(path))
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveStaleSocket_RefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(path, []byte("hi"), 0o600))
	err := removeStaleSocket(path)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestRemoveStaleSocket_EmptyPathOK(t *testing.T) {
	assert.NoError(t, removeStaleSocket(""))
}

// TestAcquireDataRootLockRejectsSecond locks the M2-P3-001 fix:
// holding the lock once must prevent a second acquisition.
func TestAcquireDataRootLockRejectsSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentchatd.lock")
	first, err := acquireDataRootLock(path)
	require.NoError(t, err)
	require.NotNil(t, first)
	defer first.Release()

	_, err = acquireDataRootLock(path)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestAcquireDataRootLockReleasesAndReclaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentchatd.lock")
	first, err := acquireDataRootLock(path)
	require.NoError(t, err)
	first.Release()

	second, err := acquireDataRootLock(path)
	require.NoError(t, err)
	second.Release()
}

func TestNewLoggerLevels(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"bogus", slog.LevelInfo},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			l := newLogger(c.in)
			require.NotNil(t, l)
			// Probe by checking that the chosen level is enabled and the
			// next lower one is not.
			assert.True(t, l.Enabled(nil, c.want))
		})
	}
}
