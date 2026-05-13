// Package crypto provides AES-GCM symmetric encryption (used to protect
// Discord bot tokens at rest) and bcrypt hashing (used for API tokens).
// The AES master key is loaded from disk at daemon startup. See
// docs/03-architecture.md D4 and D6.
package crypto
