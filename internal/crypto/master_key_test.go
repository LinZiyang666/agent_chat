package crypto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateMasterKeyBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	k1, err := LoadOrCreateMasterKey(path)
	require.NoError(t, err)
	require.Len(t, k1, MasterKeyLen)

	// File now exists with mode 0o600.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Second call returns the same bytes.
	k2, err := LoadOrCreateMasterKey(path)
	require.NoError(t, err)
	assert.Equal(t, k1, k2)
}

func TestLoadOrCreateMasterKeyCreatesParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "master.key")
	_, err := LoadOrCreateMasterKey(path)
	require.NoError(t, err)
	_, err = os.Stat(path)
	assert.NoError(t, err)
}

func TestLoadOrCreateMasterKeyRejectsBadLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	require.NoError(t, os.WriteFile(path, []byte("too short"), 0o600))
	_, err := LoadOrCreateMasterKey(path)
	assert.Error(t, err)
}

func TestLoadOrCreateMasterKeyRejectsEmptyPath(t *testing.T) {
	_, err := LoadOrCreateMasterKey("")
	assert.Error(t, err)
}

// M8 S-P1-002 regression: an existing master.key whose mode was
// loosened by a backup restore or operator chmod must be re-tightened
// to 0o600 on every load, not silently accepted.
func TestLoadOrCreateMasterKeyRetightensExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	// Plant a valid-length key with world-readable mode.
	key := make([]byte, MasterKeyLen)
	for i := range key {
		key[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(path, key, 0o644))
	// Re-chmod after WriteFile in case umask narrowed it.
	require.NoError(t, os.Chmod(path, 0o644))

	got, err := LoadOrCreateMasterKey(path)
	require.NoError(t, err)
	assert.Equal(t, key, got, "loaded bytes equal planted bytes")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"mode must be tightened to 0o600 even when the file already existed")
}
