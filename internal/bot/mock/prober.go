package mock

import (
	"context"
	"errors"
	"sync"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Prober is a deterministic, in-memory bot.IdentityProber for tests.
// It mirrors the existing mock.Provider convention: username defaults
// to the caller's hint.Username (which the API layer fills with the
// agentchat account.Name), so a "vanilla" set-discord call passes the
// name-match check by default. Tests that want to exercise the
// CONFLICT / force-rename branches can stash an explicit identity
// against a token with SetIdentity, then Probe will return that
// instead.
type Prober struct {
	mu           sync.Mutex
	overrides    map[string]bot.Identity
	renameErr    error
	renameCalled []RenameCall
}

// RenameCall records what a test's force-rename branch asked for.
type RenameCall struct {
	Token       string
	NewUsername string
}

// NewProber returns a fresh prober with no overrides and no rename
// error. Each test should construct its own to avoid sharing state.
func NewProber() *Prober {
	return &Prober{overrides: map[string]bot.Identity{}}
}

// SetIdentity makes Probe return `id` for the given token instead of
// echoing the hint. Use this from tests that need to drive a username
// mismatch.
func (p *Prober) SetIdentity(token string, id bot.Identity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.overrides[token] = id
}

// SetRenameError forces the next Rename call (and all subsequent ones)
// to return err. Pass nil to clear.
func (p *Prober) SetRenameError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renameErr = err
}

// RenameCalls returns the rename history (in call order) and resets
// the internal log. Tests assert here to verify force-rename actually
// fired with the expected username.
func (p *Prober) RenameCalls() []RenameCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]RenameCall(nil), p.renameCalled...)
	p.renameCalled = nil
	return out
}

// Probe satisfies bot.IdentityProber. Empty tokens are an
// InvalidArgument; otherwise the override (if any) wins, else the
// hint is echoed back as `Identity{UserID: "u-"+hint.Username, ...}`
// — the same shape mock.Provider produces, so set-discord followed by
// online keeps the bot_user_id stable.
func (p *Prober) Probe(_ context.Context, token string, hint bot.Identity) (bot.Identity, error) {
	if token == "" {
		return bot.Identity{}, errcode.New(errcode.InvalidArgument, "empty token")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.overrides[token]; ok {
		return id, nil
	}
	return bot.Identity{
		UserID:   "u-" + hint.Username,
		Username: hint.Username,
	}, nil
}

// Rename satisfies bot.IdentityProber. Logs the call and either
// updates the override (so the next Probe returns the new username)
// or returns the stashed error.
func (p *Prober) Rename(_ context.Context, token string, newUsername string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renameCalled = append(p.renameCalled, RenameCall{Token: token, NewUsername: newUsername})
	if p.renameErr != nil {
		return p.renameErr
	}
	if newUsername == "" {
		return errors.New("mock prober: empty newUsername")
	}
	id := p.overrides[token]
	id.Username = newUsername
	if id.UserID == "" {
		id.UserID = "u-" + newUsername
	}
	p.overrides[token] = id
	return nil
}
