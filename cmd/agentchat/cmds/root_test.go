package cmds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetTokenSources clears the package-level flag/env state so each
// test starts from a known-empty baseline. t.Setenv cleans env on
// teardown; we also clear the flag pointer.
func resetTokenSources(t *testing.T) {
	t.Helper()
	prev := flagToken
	flagToken = ""
	t.Cleanup(func() { flagToken = prev })
	t.Setenv(envToken, "")
	t.Setenv(envHome, "")
}

func TestResolveTokenFlagWins(t *testing.T) {
	resetTokenSources(t)
	flagToken = "from-flag"
	t.Setenv(envToken, "from-env")
	assert.Equal(t, "from-flag", resolveToken())
}

func TestResolveTokenEnvBeatsFile(t *testing.T) {
	resetTokenSources(t)
	dir := t.TempDir()
	t.Setenv(envHome, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cli.toml"),
		[]byte(`token = "from-file"`), 0o600))
	t.Setenv(envToken, "from-env")
	assert.Equal(t, "from-env", resolveToken())
}

// TestResolveTokenFromConfigFile locks the M2-P3-008 fix: cli.toml is
// a real token source when neither --token nor $AGENTCHAT_TOKEN is set.
func TestResolveTokenFromConfigFile(t *testing.T) {
	resetTokenSources(t)
	dir := t.TempDir()
	t.Setenv(envHome, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cli.toml"),
		[]byte(`token = "from-file"`), 0o600))
	assert.Equal(t, "from-file", resolveToken())
}

func TestResolveTokenMissingFileIsEmpty(t *testing.T) {
	resetTokenSources(t)
	t.Setenv(envHome, t.TempDir())
	assert.Equal(t, "", resolveToken())
}

func TestResolveTokenMalformedFileIsEmpty(t *testing.T) {
	resetTokenSources(t)
	dir := t.TempDir()
	t.Setenv(envHome, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cli.toml"),
		[]byte("not = valid = toml\n"), 0o600))
	// Malformed config is intentionally silent — the daemon will surface
	// AUTH_MISSING if the user really has no token. See resolveToken doc.
	assert.Equal(t, "", resolveToken())
}

func TestResolveSocketUsesDataRoot(t *testing.T) {
	resetTokenSources(t)
	dir := t.TempDir()
	t.Setenv(envHome, dir)
	assert.Equal(t, filepath.Join(dir, "agentchatd.sock"), resolveSocket())
}

func TestResolveSocketEnvBeatsDataRoot(t *testing.T) {
	resetTokenSources(t)
	t.Setenv(envHome, "/some/root")
	t.Setenv(envSocket, "/custom/path.sock")
	assert.Equal(t, "/custom/path.sock", resolveSocket())
}
