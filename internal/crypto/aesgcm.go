package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// AESGCMEncrypt encrypts plaintext with key (must be MasterKeyLen
// bytes) using AES-256-GCM. The returned blob is nonce || ciphertext,
// suitable for storing in a BLOB column. The same key + blob roundtrips
// through AESGCMDecrypt.
func AESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "read nonce")
	}
	// Seal appends ciphertext+tag onto nonce, producing the canonical
	// blob layout we persist.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// AESGCMDecrypt reverses AESGCMEncrypt. Returns AuthInvalid (via
// errcode) on tampered ciphertext or wrong key — useful so the API
// can map decryption failure to a sane error code.
func AESGCMDecrypt(key, encrypted []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(encrypted) < ns {
		return nil, errcode.New(errcode.InvalidArgument, "encrypted blob shorter than nonce")
	}
	nonce, ciphertext := encrypted[:ns], encrypted[ns:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// gcm.Open fails on tampering or wrong key — treat as
		// AuthInvalid so callers can branch.
		return nil, errcode.Wrap(err, errcode.AuthInvalid, "decrypt")
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != MasterKeyLen {
		return nil, errcode.New(errcode.InvalidArgument,
			"key must be %d bytes, got %d", MasterKeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "aes new cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "new gcm")
	}
	return gcm, nil
}
