package crypto

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// MasterKeyLen is the size in bytes of the AES master key. 32 bytes =
// AES-256.
const MasterKeyLen = 32

// LoadOrCreateMasterKey returns the master encryption key at path. If
// the file does not exist, a fresh random key is generated and written
// with mode 0o600. The caller is responsible for the surrounding
// directory's permissions.
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errcode.New(errcode.InvalidArgument, "master-key path is empty")
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != MasterKeyLen {
			return nil, errcode.New(errcode.Internal,
				"master key at %s has wrong length: got %d want %d",
				path, len(data), MasterKeyLen)
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errcode.Wrap(err, errcode.Internal, "read master key %s", path)
	}

	// Bootstrap: parent directory should already exist (config.EnsureDataRoot),
	// but be lenient and create it if missing.
	if dir := filepath.Dir(path); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return nil, errcode.Wrap(mkErr, errcode.Internal, "create dir %s", dir)
		}
	}

	key := make([]byte, MasterKeyLen)
	if _, rerr := rand.Read(key); rerr != nil {
		return nil, errcode.Wrap(rerr, errcode.Internal, "generate master key")
	}
	// 0o600: only the owning user can read; the key never leaves disk.
	if werr := os.WriteFile(path, key, 0o600); werr != nil {
		return nil, errcode.Wrap(werr, errcode.Internal, "write master key %s", path)
	}
	return key, nil
}
