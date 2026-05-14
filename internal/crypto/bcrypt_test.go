package crypto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

func TestHashAndVerifyAPIToken(t *testing.T) {
	secret, err := RandomSecret()
	require.NoError(t, err)
	require.NotEmpty(t, secret)

	hash, err := HashAPIToken(secret)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	assert.NoError(t, VerifyAPIToken(hash, secret))
}

func TestVerifyWrongSecretIsAuthInvalid(t *testing.T) {
	secret, _ := RandomSecret()
	hash, _ := HashAPIToken(secret)

	other, _ := RandomSecret()
	err := VerifyAPIToken(hash, other)
	require.Error(t, err)

	e, ok := errcode.As(err)
	require.True(t, ok)
	assert.Equal(t, errcode.AuthInvalid, e.Code)
}

func TestHashRejectsEmpty(t *testing.T) {
	_, err := HashAPIToken("")
	require.Error(t, err)
	e, ok := errcode.As(err)
	require.True(t, ok)
	assert.Equal(t, errcode.InvalidArgument, e.Code)
}

func TestRandomSecretUnique(t *testing.T) {
	const n = 50
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		s, err := RandomSecret()
		require.NoError(t, err)
		_, dup := seen[s]
		assert.False(t, dup, "duplicate secret on iteration %d", i)
		seen[s] = struct{}{}
	}
}

func TestRandomSecretLengthBase64URL(t *testing.T) {
	s, err := RandomSecret()
	require.NoError(t, err)
	// 32 bytes raw → 43 base64-url chars without padding.
	assert.Len(t, s, 43)
}
