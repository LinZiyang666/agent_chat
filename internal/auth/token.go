// Package auth issues and verifies API tokens used by the CLI to
// authenticate to the daemon. Tokens have the form
//
//	agch_<token_id>_<secret>
//
// where token_id is a UUIDv7 in compact hex (32 chars, no dashes) that
// is *not* secret on its own, and secret is 32 random bytes encoded
// as 43-character base64url. The server stores only the bcrypt hash of
// secret, never the secret itself.
//
// On every request, the daemon splits the raw token, looks the row up
// by id in O(1), and verifies secret with bcrypt. The format is
// directly inspired by GitHub Personal Access Tokens.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/crypto"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// TokenPrefix is the literal prefix every raw token starts with.
const TokenPrefix = "agch_"

// encodeRaw builds the wire form of a token. Exposed for tests; the
// canonical producer is Manager.Issue.
func encodeRaw(id, secret string) string {
	return TokenPrefix + id + "_" + secret
}

// parseRaw splits the wire form into (id, secret). It rejects anything
// that does not strictly match the format with a structured AuthInvalid
// error — agents can branch on the code without parsing a message.
func parseRaw(raw string) (id, secret string, err error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return "", "", errcode.New(errcode.AuthInvalid, "token has bad prefix")
	}
	rest := raw[len(TokenPrefix):]
	sep := strings.IndexByte(rest, '_')
	if sep < 0 {
		return "", "", errcode.New(errcode.AuthInvalid, "token missing separator")
	}
	id = rest[:sep]
	secret = rest[sep+1:]
	if id == "" || secret == "" {
		return "", "", errcode.New(errcode.AuthInvalid, "token has empty id or secret")
	}
	return id, secret, nil
}

// Manager wires the token store to a bcrypt-based verifier. Construct
// one per daemon; it is safe for concurrent use.
type Manager struct {
	tokens store.TokenRepo
	// now is injected for tests; production uses time.Now().UTC().
	now func() time.Time
}

// NewManager returns a Manager backed by tokens.
func NewManager(tokens store.TokenRepo) *Manager {
	return &Manager{tokens: tokens, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source. Tests only.
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// Issue creates a fresh API token for accountID and returns both the
// raw wire form (caller must display once and discard) and the persisted
// row.
func (m *Manager) Issue(ctx context.Context, accountID string) (string, *store.Token, error) {
	return m.IssueVia(ctx, m.tokens, accountID)
}

// IssueVia is Issue but persists the new token via the given repo. Used
// by handlers that run mutation + audit inside a single transaction
// (see store.Bundler.WithTx).
//
// Implementation note: PrepareToken (slow, bcrypt) runs **outside**
// the transaction caller's lock, so high-cost CPU work does not hold
// the SQLite single-writer connection. PersistTokenVia is then just an
// INSERT, fast enough to run inside the tx.
func (m *Manager) IssueVia(ctx context.Context, repo store.TokenRepo, accountID string) (string, *store.Token, error) {
	raw, t, err := m.PrepareToken(accountID)
	if err != nil {
		return "", nil, err
	}
	if err := m.PersistTokenVia(ctx, repo, t); err != nil {
		return "", nil, err
	}
	return raw, t, nil
}

// PrepareToken generates the raw wire form, id, secret, and bcrypt
// hash for a brand-new API token. **No I/O is performed.** Callers
// running a tx for mutation+audit should call PrepareToken first
// (slow, bcrypt cost ~150ms) and only enter `WithTx` for the fast
// INSERT via PersistTokenVia.
//
// This split was introduced for M2-P3-014: previously the bcrypt
// computation ran inside the SQLite tx, holding the single-writer
// connection for the bcrypt duration and serializing all other
// writes against token issuance.
func (m *Manager) PrepareToken(accountID string) (string, *store.Token, error) {
	if accountID == "" {
		return "", nil, errcode.New(errcode.InvalidArgument, "accountID is empty")
	}
	u, err := uuid.NewV7()
	if err != nil {
		return "", nil, errcode.Wrap(err, errcode.Internal, "uuidv7")
	}
	id := strings.ReplaceAll(u.String(), "-", "")

	secret, err := crypto.RandomSecret()
	if err != nil {
		return "", nil, err
	}
	hash, err := crypto.HashAPIToken(secret)
	if err != nil {
		return "", nil, err
	}

	return encodeRaw(id, secret), &store.Token{
		ID:        id,
		AccountID: accountID,
		Hash:      hash,
		CreatedAt: m.now(),
	}, nil
}

// PersistTokenVia writes a prepared token row via the given repo. Fast
// path — no bcrypt, just an INSERT. Returns the underlying repo error
// unchanged.
func (m *Manager) PersistTokenVia(ctx context.Context, repo store.TokenRepo, t *store.Token) error {
	if repo == nil {
		return errcode.New(errcode.Internal, "token repo is nil")
	}
	if t == nil {
		return errcode.New(errcode.InvalidArgument, "token is nil")
	}
	return repo.Create(ctx, t)
}

// Verify parses raw, looks up the token row, and bcrypt-verifies the
// secret. On success it returns the bound accountID and the token ID,
// and best-effort updates last_used_at. Any verification failure maps
// to a structured Auth* error.
func (m *Manager) Verify(ctx context.Context, raw string) (accountID, tokenID string, err error) {
	id, secret, err := parseRaw(raw)
	if err != nil {
		return "", "", err
	}
	t, err := m.tokens.Get(ctx, id)
	if err != nil {
		if e, ok := errcode.As(err); ok && e.Code == errcode.NotFound {
			// Hide the existence of token IDs from probers.
			return "", "", errcode.New(errcode.AuthInvalid, "no such token")
		}
		return "", "", err
	}
	if t.Revoked() {
		return "", "", errcode.New(errcode.AuthRevoked, "token revoked")
	}
	if err := crypto.VerifyAPIToken(t.Hash, secret); err != nil {
		return "", "", err
	}
	// Best-effort: don't deny access if last-used cannot be persisted.
	_ = m.tokens.TouchLastUsed(ctx, id, m.now())
	return t.AccountID, id, nil
}

// Revoke marks the given token id as revoked. Subsequent Verify on a
// raw token whose id matches will return AuthRevoked.
func (m *Manager) Revoke(ctx context.Context, id string) error {
	return m.RevokeVia(ctx, m.tokens, id)
}

// RevokeVia is Revoke but acts on the given repo (used inside WithTx
// closures to share a transaction with an audit insert).
func (m *Manager) RevokeVia(ctx context.Context, repo store.TokenRepo, id string) error {
	if repo == nil {
		return errcode.New(errcode.Internal, "token repo is nil")
	}
	return repo.Revoke(ctx, id, m.now())
}
