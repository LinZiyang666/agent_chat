// Package crypto provides the cryptographic primitives the daemon needs:
// AES-GCM symmetric encryption (for Discord bot tokens at rest, used from
// M3 onward) and bcrypt hashing (for API tokens, used in M2).
package crypto

import (
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// APITokenCost is the bcrypt cost used for API-token hashes.
//
// Why 12 (not the package default of 10): API tokens are long-lived
// credentials that survive an entire agent's lifetime, so we accept a
// slightly slower verification (~150ms on commodity hardware) in
// exchange for stronger brute-force resistance. CLI auth latency stays
// well below the human-perception threshold even at this cost.
const APITokenCost = 12

// SecretLen is the number of random bytes used in the secret half of an
// API token (the bcrypt-hashed portion). 32 bytes (256 bits) is far
// above any near-future brute-force capability and yields a base64url
// string of 43 characters.
const SecretLen = 32

// HashAPIToken bcrypts the raw token secret and returns the hash. The
// returned bytes are safe to store as a SQL TEXT column.
func HashAPIToken(secret string) ([]byte, error) {
	if secret == "" {
		return nil, errcode.New(errcode.InvalidArgument, "secret is empty")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(secret), APITokenCost)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "bcrypt hash")
	}
	return h, nil
}

// VerifyAPIToken returns nil iff secret matches hash, or an errcode
// AuthInvalid otherwise. Other unexpected errors are wrapped as
// Internal.
func VerifyAPIToken(hash []byte, secret string) error {
	err := bcrypt.CompareHashAndPassword(hash, []byte(secret))
	if err == nil {
		return nil
	}
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return errcode.New(errcode.AuthInvalid, "token does not match")
	}
	return errcode.Wrap(err, errcode.Internal, "bcrypt verify")
}

// RandomSecret returns a base64url-encoded random secret of SecretLen
// bytes (43 chars, no padding). Used as the secret half of an API
// token.
func RandomSecret() (string, error) {
	buf := make([]byte, SecretLen)
	if _, err := rand.Read(buf); err != nil {
		return "", errcode.Wrap(err, errcode.Internal, "rand.Read")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
