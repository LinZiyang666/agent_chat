package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

func freshKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, MasterKeyLen)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func TestAESGCMRoundTrip(t *testing.T) {
	k := freshKey(t)
	pt := []byte("hello, agentchat M3 secret token")

	ct, err := AESGCMEncrypt(k, pt)
	require.NoError(t, err)
	assert.NotEqual(t, pt, ct[12:], "ciphertext must not equal plaintext after nonce prefix")

	got, err := AESGCMDecrypt(k, ct)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(pt, got))
}

func TestAESGCMNonceVaries(t *testing.T) {
	k := freshKey(t)
	c1, err := AESGCMEncrypt(k, []byte("same"))
	require.NoError(t, err)
	c2, err := AESGCMEncrypt(k, []byte("same"))
	require.NoError(t, err)
	assert.False(t, bytes.Equal(c1, c2), "successive encrypts must use a fresh nonce")
}

func TestAESGCMWrongKeyFails(t *testing.T) {
	k1 := freshKey(t)
	k2 := freshKey(t)
	ct, err := AESGCMEncrypt(k1, []byte("payload"))
	require.NoError(t, err)
	_, err = AESGCMDecrypt(k2, ct)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthInvalid, e.Code)
}

func TestAESGCMTamperedCiphertextFails(t *testing.T) {
	k := freshKey(t)
	ct, err := AESGCMEncrypt(k, []byte("payload"))
	require.NoError(t, err)
	// Flip a byte in the ciphertext (after the 12-byte nonce).
	tampered := append([]byte{}, ct...)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = AESGCMDecrypt(k, tampered)
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.AuthInvalid, e.Code)
}

func TestAESGCMShortBlobRejected(t *testing.T) {
	k := freshKey(t)
	_, err := AESGCMDecrypt(k, []byte("short"))
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestAESGCMWrongKeySize(t *testing.T) {
	_, err := AESGCMEncrypt([]byte("too short"), []byte("x"))
	require.Error(t, err)
	e, _ := errcode.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestAESGCMEmptyPlaintext(t *testing.T) {
	k := freshKey(t)
	ct, err := AESGCMEncrypt(k, []byte{})
	require.NoError(t, err)
	got, err := AESGCMDecrypt(k, ct)
	require.NoError(t, err)
	assert.Equal(t, 0, len(got))
}
