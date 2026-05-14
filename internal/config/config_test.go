package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaults(t *testing.T) {
	c := Defaults("/tmp/agentchat-test")
	assert.Equal(t, "/tmp/agentchat-test", c.DataRoot)
	assert.Equal(t, "/tmp/agentchat-test/agentchatd.sock", c.SocketPath)
	assert.Equal(t, "/tmp/agentchat-test/agentchatd.db", c.DBPath)
	assert.Equal(t, "/tmp/agentchat-test/master.key", c.KeyPath)
	assert.Equal(t, "info", c.Log.Level)
}

func TestDefaultsEmptyUsesHome(t *testing.T) {
	t.Setenv("HOME", "/home/fake")
	c := Defaults("")
	assert.Equal(t, "/home/fake/.agentchat", c.DataRoot)
}

func TestLoadFromArgumentBeatsEnv(t *testing.T) {
	t.Setenv(EnvHome, "/envdir")
	cfg, err := Load("/argdir")
	require.NoError(t, err)
	assert.Equal(t, "/argdir", cfg.DataRoot)
}

func TestLoadFromEnvWhenArgEmpty(t *testing.T) {
	t.Setenv(EnvHome, "/envdir")
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "/envdir", cfg.DataRoot)
}

func TestLoadTOMLOverlay(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
data_root = "`+dir+`"

[log]
level = "debug"
`), 0o600))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.DataRoot)
	assert.Equal(t, "debug", cfg.Log.Level)
}

func TestLoadTOMLMissingIsOK(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.DataRoot)
}

func TestLoadTOMLMalformed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not = valid = toml\n"), 0o600))
	_, err := Load(dir)
	assert.Error(t, err)
}

func TestEnvOverrideSocket(t *testing.T) {
	t.Setenv(EnvSocket, "/custom/path.sock")
	dir := t.TempDir()
	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "/custom/path.sock", cfg.SocketPath)
}

func TestEnvOverrideLogLevel(t *testing.T) {
	t.Setenv(EnvLogLevel, "warn")
	dir := t.TempDir()
	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "warn", cfg.Log.Level)
}

func TestFinalizeMakesAbsolute(t *testing.T) {
	c := Config{DataRoot: "./relroot"}
	require.NoError(t, c.finalize())
	assert.True(t, filepath.IsAbs(c.DataRoot), "DataRoot must be absolute after finalize")
}

func TestFinalizeRejectsEmptyDataRoot(t *testing.T) {
	c := Config{}
	assert.Error(t, c.finalize())
}

func TestEnsureDataRoot(t *testing.T) {
	dir := t.TempDir()
	c := Defaults(filepath.Join(dir, "nested", "subdir"))
	require.NoError(t, c.EnsureDataRoot())

	info, err := os.Stat(c.DataRoot)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	// Mode check is unreliable on some FS; just verify it exists.
}

func TestString(t *testing.T) {
	c := Defaults("/tmp/x")
	s := c.String()
	assert.Contains(t, s, "DataRoot=/tmp/x")
	assert.Contains(t, s, "Socket=/tmp/x/agentchatd.sock")
}

// TestEnsureDataRootTightensExistingDir locks the M2-P3-005 fix: an
// existing 0o755 directory becomes 0o700 after EnsureDataRoot runs.
func TestEnsureDataRootTightensExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	c := Defaults(dir)
	require.NoError(t, c.EnsureDataRoot())
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"EnsureDataRoot must tighten existing dir perms to 0o700")
}

// TestLoadTOMLDataRootRebasesPaths locks the M2-P3-006 fix: a config
// whose data_root differs from the resolved one must produce socket /
// db / key paths rooted under the *new* directory.
func TestLoadTOMLDataRootRebasesPaths(t *testing.T) {
	base := t.TempDir()
	next := t.TempDir()

	cfgPath := filepath.Join(base, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`data_root = "`+next+`"`), 0o600))

	cfg, err := Load(base)
	require.NoError(t, err)
	assert.Equal(t, next, cfg.DataRoot)
	assert.Equal(t, filepath.Join(next, "agentchatd.sock"), cfg.SocketPath)
	assert.Equal(t, filepath.Join(next, "agentchatd.db"), cfg.DBPath)
	assert.Equal(t, filepath.Join(next, "master.key"), cfg.KeyPath)
}

// TestLoadExplicitTOMLSocketOverridesRebase confirms explicit socket
// in TOML still wins over the rebased default.
func TestLoadExplicitTOMLSocketOverridesRebase(t *testing.T) {
	base := t.TempDir()
	next := t.TempDir()
	custom := filepath.Join(next, "custom.sock")

	cfgPath := filepath.Join(base, "config.toml")
	body := "data_root = \"" + next + "\"\nsocket_path = \"" + custom + "\"\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))

	cfg, err := Load(base)
	require.NoError(t, err)
	assert.Equal(t, next, cfg.DataRoot)
	assert.Equal(t, custom, cfg.SocketPath)
}
