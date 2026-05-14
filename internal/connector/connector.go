// Package connector owns the lifecycle of bot.Provider instances and
// the fan-out of their events to subscribers.
//
// Layering note (M3): the Connector intentionally does NOT touch the
// SQLite store. Account-state and audit-log writes for online/offline
// transitions are the API handler's responsibility, so they can run
// inside `store.Bundler.WithTx` and stay consistent with the M2-P3-012
// architectural fix. The Connector only manages the in-memory state
// of live Providers and the event fan-out.
//
// Concurrency model:
//
//   - Each account has at most one *instance entry in `instances`,
//     keyed by accountID. The instance carries a state (`connecting`,
//     `online`, `disconnecting`) that gates concurrent Connect and
//     Disconnect calls. Connect reserves the slot with `connecting`
//     before unlocking, so a second concurrent Connect sees the slot
//     and returns Conflict — preventing the duplicate-Provider race
//     reported as M3-P3-003.
//
//   - Disconnect transitions to `disconnecting` while holding the
//     mutex, then unlocks for the slow Provider.Disconnect call, then
//     re-locks to remove the entry. This avoids deleting the entry
//     before the underlying connection is actually torn down
//     (a concern raised in M3-P3-002 audit guidance).
//
//   - `pump` holds the mutex while delivering events to subscribers
//     (sends are non-blocking with a `default:` so holding the lock
//     is safe). Unsubscribe also holds the mutex when closing the
//     subscription channel. This eliminates the send-on-closed-channel
//     race reported as M3-P3-004.
package connector

import (
	"context"
	"log/slog"
	"sync"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Factory constructs a Provider given a decrypted bot token and an
// identity hint (typically the account's display name). The returned
// Provider must not be Connect()'d yet — Connector calls Connect.
type Factory func(token string, hint bot.Identity) bot.Provider

// instanceState is the lifecycle phase of a per-account map entry.
type instanceState int

const (
	stateConnecting instanceState = iota + 1
	stateOnline
	stateDisconnecting
)

// instance is the per-account state Connector stores in `instances`.
// `p` is nil while state == stateConnecting (the Factory has not
// returned yet, or the Provider has not been Connect()'d).
type instance struct {
	p     bot.Provider
	state instanceState
}

// Connector tracks live Providers by account id and broadcasts each
// Provider's events to its subscribers.
type Connector struct {
	mu        sync.Mutex
	instances map[string]*instance
	subs      map[string][]*Subscription
	factory   Factory
	log       *slog.Logger
}

// New builds an empty Connector.
func New(factory Factory, log *slog.Logger) *Connector {
	if log == nil {
		log = slog.Default()
	}
	return &Connector{
		instances: map[string]*instance{},
		subs:      map[string][]*Subscription{},
		factory:   factory,
		log:       log,
	}
}

// Connect builds and Connect()s a Provider for accountID.
//
// The slot in `instances` is reserved before the slow Provider.Connect
// call so a concurrent Connect for the same accountID returns
// `errcode.Conflict` rather than racing past the pre-check (M3-P3-003).
func (c *Connector) Connect(ctx context.Context, accountID, token string, hint bot.Identity) error {
	c.mu.Lock()
	if _, ok := c.instances[accountID]; ok {
		c.mu.Unlock()
		return errcode.New(errcode.Conflict,
			"account %s already has a live or in-flight provider", accountID)
	}
	// Reserve the slot. Any concurrent Connect/Disconnect for this
	// accountID will see the entry and bail out with Conflict.
	c.instances[accountID] = &instance{state: stateConnecting}
	c.mu.Unlock()

	p := c.factory(token, hint)
	if err := p.Connect(ctx); err != nil {
		c.mu.Lock()
		delete(c.instances, accountID)
		c.mu.Unlock()
		return err
	}

	c.mu.Lock()
	inst := c.instances[accountID]
	if inst == nil {
		// Defensive: someone Shutdown'd or deleted between our slow
		// Connect and re-locking. Treat as conflict; tear down the
		// just-built Provider.
		c.mu.Unlock()
		_ = p.Disconnect(context.Background())
		return errcode.New(errcode.Conflict,
			"account %s slot was cleared during connect", accountID)
	}
	inst.p = p
	inst.state = stateOnline
	c.mu.Unlock()

	go c.pump(accountID, p)
	return nil
}

// Disconnect tears down the Provider for accountID. Returns Conflict
// if the account has no live Provider or is in the middle of another
// lifecycle transition.
//
// Implementation order (M3-P3-002 audit guidance): transition the slot
// to `disconnecting` while holding the mutex, drop the lock for the
// slow Provider.Disconnect call, then re-lock to commit the outcome.
//
// Error policy (M3-P3-007 audit guidance): if Provider.Disconnect
// returns an error the external connection may still be live. We
// restore the slot to `online` and keep the map entry so a retry of
// Disconnect remains reachable, IsRegistered still protects against
// account.delete, and Provider() still returns the live handle for
// the operator. Only a successful Provider.Disconnect deletes the
// entry.
func (c *Connector) Disconnect(ctx context.Context, accountID string) error {
	c.mu.Lock()
	inst, ok := c.instances[accountID]
	if !ok {
		c.mu.Unlock()
		return errcode.New(errcode.Conflict, "account %s has no live provider", accountID)
	}
	if inst.state != stateOnline {
		c.mu.Unlock()
		return errcode.New(errcode.Conflict,
			"account %s is in transition (state=%d); retry", accountID, inst.state)
	}
	inst.state = stateDisconnecting
	p := inst.p
	c.mu.Unlock()

	err := p.Disconnect(ctx)

	c.mu.Lock()
	if err != nil {
		// Rollback: keep the entry so the operator can retry. The
		// underlying connection may or may not be live; the next
		// Disconnect call will resolve it (a Provider whose internal
		// state is already closed will return nil quickly).
		inst.state = stateOnline
	} else {
		delete(c.instances, accountID)
	}
	c.mu.Unlock()

	if err != nil {
		c.log.Warn("provider disconnect returned error; entry kept for retry",
			"account_id", accountID, "err", err)
		return err
	}
	return nil
}

// Status returns the live Provider's connection status for accountID,
// or StatusOffline if none is registered. A Provider that is mid-Connect
// or mid-Disconnect reports StatusConnecting / StatusOffline based on
// the Provider's own self-report when available.
func (c *Connector) Status(accountID string) bot.ConnStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	inst, ok := c.instances[accountID]
	if !ok {
		return bot.StatusOffline
	}
	if inst.p == nil {
		return bot.StatusConnecting
	}
	return inst.p.Status()
}

// Provider returns the live Provider for accountID, or (nil, false)
// if the account is not fully online.
func (c *Connector) Provider(accountID string) (bot.Provider, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	inst, ok := c.instances[accountID]
	if !ok || inst.state != stateOnline || inst.p == nil {
		return nil, false
	}
	return inst.p, true
}

// IsRegistered reports whether the account currently has any
// in-memory entry (connecting / online / disconnecting). Used by API
// handlers that need to refuse account.delete while a Provider is
// still live (M3-P3-001).
func (c *Connector) IsRegistered(accountID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.instances[accountID]
	return ok
}

// Shutdown disconnects every live Provider. Errors are logged rather
// than returned (caller is already on the shutdown path).
func (c *Connector) Shutdown(ctx context.Context) {
	c.mu.Lock()
	ids := make([]string, 0, len(c.instances))
	for id, inst := range c.instances {
		if inst.state == stateOnline {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	for _, id := range ids {
		if err := c.Disconnect(ctx, id); err != nil {
			c.log.Warn("shutdown disconnect failed", "account_id", id, "err", err)
		}
	}
}

// Subscription delivers a Provider's events to one consumer. The
// underlying channel is closed exactly once — either when Unsubscribe
// is called or when the Provider disconnects and the pump exits.
type Subscription struct {
	C         <-chan bot.Event
	send      chan bot.Event
	accountID string
	closed    bool
}

// AccountID is the account this subscription is bound to.
func (s *Subscription) AccountID() string { return s.accountID }

// Subscribe registers a new event consumer for accountID. Unsubscribe
// must be called when the consumer is done; the channel is otherwise
// kept alive (and may drop events if not drained).
func (c *Connector) Subscribe(accountID string) *Subscription {
	ch := make(chan bot.Event, 32)
	s := &Subscription{C: ch, send: ch, accountID: accountID}
	c.mu.Lock()
	c.subs[accountID] = append(c.subs[accountID], s)
	c.mu.Unlock()
	return s
}

// Unsubscribe removes s from the fan-out and closes its channel.
// Holds c.mu while closing, so the close serializes with pump's
// guarded send (M3-P3-004 fix). Safe to call multiple times.
func (c *Connector) Unsubscribe(s *Subscription) {
	if s == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	arr := c.subs[s.accountID]
	for i, x := range arr {
		if x == s {
			c.subs[s.accountID] = append(arr[:i], arr[i+1:]...)
			break
		}
	}
	if !s.closed {
		s.closed = true
		close(s.send)
	}
}

// pump consumes events from a Provider and fans them out to current
// subscribers. The dispatch loop holds c.mu while iterating and
// sending so concurrent Unsubscribe (which closes channels under the
// same mutex) cannot race with our send (M3-P3-004 fix). Sends are
// non-blocking (`select … default`) so holding the lock is bounded.
//
// When the Provider's events channel closes, every remaining
// subscriber is closed too — under the same mutex.
func (c *Connector) pump(accountID string, p bot.Provider) {
	for ev := range p.Events() {
		c.mu.Lock()
		for _, s := range c.subs[accountID] {
			if s.closed {
				continue
			}
			select {
			case s.send <- ev:
			default:
				// Slow subscriber — drop.
			}
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	for _, s := range c.subs[accountID] {
		if !s.closed {
			s.closed = true
			close(s.send)
		}
	}
	delete(c.subs, accountID)
	c.mu.Unlock()
}
