package discord

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Prober is the Discord-backed implementation of bot.IdentityProber.
// It only uses REST (no gateway WebSocket); a single GET /users/@me is
// enough to verify a token and read the bot's current username.
type Prober struct{}

// NewProber returns a stateless prober. Concurrent calls are safe.
func NewProber() *Prober { return &Prober{} }

// Probe authenticates `token` by calling GET /users/@me with
// Authorization: Bot <token>. Returns the bot's snowflake +
// username. `hint` is ignored — Discord is the source of truth.
//
// 401-equivalent failures from discordgo (bad / revoked token) are
// mapped to AuthInvalid so the API layer renders the right exit code;
// transient errors map to Unavailable so retries are obvious.
func (Prober) Probe(_ context.Context, token string, _ bot.Identity) (bot.Identity, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return bot.Identity{}, errcode.Wrap(err, errcode.AuthInvalid, "construct discord session")
	}
	// discordgo doesn't expose a context-aware variant of User(@me);
	// the underlying RequestWithBucketID call is short-lived REST,
	// and the caller's request context governs the surrounding
	// handler. A future replacement could use http.Client + manual
	// auth header if cancellation precision becomes important.
	u, err := s.User("@me")
	if err != nil {
		// Discord 401s on the auth header come through as a generic
		// error string; sniff for "401" to surface AuthInvalid.
		if strings.Contains(err.Error(), "401") {
			return bot.Identity{}, errcode.Wrap(err, errcode.AuthInvalid,
				"bot token rejected by Discord")
		}
		return bot.Identity{}, errcode.Wrap(err, errcode.Unavailable,
			"fetch /users/@me")
	}
	if u == nil || u.ID == "" {
		return bot.Identity{}, errcode.New(errcode.Unavailable,
			"discord returned empty user payload")
	}
	return bot.Identity{
		UserID:   u.ID,
		Username: u.Username,
	}, nil
}

// Rename calls PATCH /users/@me with `{username: newUsername}`. Discord
// enforces a per-bot username rate limit (2/h); rate-limited failures
// surface as Unavailable so the caller can advise a retry-after.
func (Prober) Rename(_ context.Context, token string, newUsername string) error {
	if newUsername == "" {
		return errcode.New(errcode.InvalidArgument, "newUsername is empty")
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return errcode.Wrap(err, errcode.AuthInvalid, "construct discord session")
	}
	if _, err := s.UserUpdate(newUsername, "", ""); err != nil {
		if strings.Contains(err.Error(), "401") {
			return errcode.Wrap(err, errcode.AuthInvalid,
				"bot token rejected during rename")
		}
		if strings.Contains(err.Error(), "429") {
			return errcode.Wrap(err, errcode.Unavailable,
				"discord rate-limited username rename (2/h cap)")
		}
		return errcode.Wrap(err, errcode.Unavailable, "rename bot username")
	}
	return nil
}
