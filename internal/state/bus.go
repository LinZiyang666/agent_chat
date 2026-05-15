package state

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// MaxSubscribersPerAccount caps the number of concurrent state-watch
// subscribers per account. A single token holding many concurrent
// streams would otherwise hold N goroutines + N channels + an SQLite
// connection per BuildNow indefinitely (M8-S-P2-008). 8 is generous
// for normal agents and tight enough that a runaway script cannot
// silently exhaust resources.
const MaxSubscribersPerAccount = 8

// Bus is the M5 state-fan-out engine. It:
//
//   - keeps an atomic.Int64 version counter that increments on every
//     emitted snapshot (per requirements §5.2 + roadmap §6).
//   - debounces Publish events with a 200 ms timer so a burst of
//     mutations (e.g., a /v1/rooms/{id}/messages call writes one
//     message row + N message_state rows + an audit row) collapses
//     into a single recomputation.
//   - calls Aggregator.Build inside the timer's goroutine and broadcasts
//     the snapshot to every subscriber on that account.
//   - is safe for concurrent Publish / Subscribe / Unsubscribe and is
//     never blocked by a slow consumer (sends drop on full buffer,
//     similar to the connector.pump pattern).
type Bus struct {
	agg      *Aggregator
	log      *slog.Logger
	debounce time.Duration

	version atomic.Int64

	mu      sync.Mutex
	pending map[string]*time.Timer
	subs    map[string][]*Subscription
}

// NewBus builds a Bus with the standard 200 ms debounce.
func NewBus(agg *Aggregator, log *slog.Logger) *Bus {
	return NewBusWithDebounce(agg, log, 200*time.Millisecond)
}

// NewBusWithDebounce builds a Bus with a custom debounce window.
// Tests use a smaller value to keep latency-sensitive assertions fast.
func NewBusWithDebounce(agg *Aggregator, log *slog.Logger, debounce time.Duration) *Bus {
	if log == nil {
		log = slog.Default()
	}
	return &Bus{
		agg:      agg,
		log:      log,
		debounce: debounce,
		pending:  map[string]*time.Timer{},
		subs:     map[string][]*Subscription{},
	}
}

// Subscription delivers debounced Snapshots for one (account, watcher)
// pair. C is closed exactly once: either when Unsubscribe is called or
// when the Bus is shut down.
type Subscription struct {
	C         <-chan *Snapshot
	send      chan *Snapshot
	accountID string
	closed    bool
}

// AccountID is the account this subscription is bound to.
func (s *Subscription) AccountID() string { return s.accountID }

// Subscribe registers a new state consumer for accountID and emits
// an initial snapshot synchronously (so the consumer always sees the
// current state on connect, per requirements §5.2 — "agent sees
// summary state, not history").
//
// On success the caller MUST drain s.C in a goroutine and MUST call
// Unsubscribe when done. Buffer size is small (8) — Bus is
// non-blocking on send so a slow consumer just drops snapshots.
//
// Race-safe wrt Publish (M5-P3-004 fix): the bus mutex is held across
// the entire build-initial + register sequence. Publishes that
// arrive BEFORE this critical section see no subscriber and drop —
// the work they reflect is already captured in the initial snapshot
// because BuildNow reads committed state. Publishes that arrive
// AFTER Subscribe returns see the registered subscriber, schedule a
// debounced rebuild, and arrive on the channel AFTER the initial
// frame (version-monotonic since fire's BuildNow also serializes on
// the mutex via buildNowLocked).
func (b *Bus) Subscribe(ctx context.Context, accountID string) (*Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs[accountID]) >= MaxSubscribersPerAccount {
		return nil, errcode.New(errcode.ResourceExhausted,
			"account %s already has %d active state subscribers (cap %d)",
			accountID, len(b.subs[accountID]), MaxSubscribersPerAccount)
	}
	initial, err := b.buildNowLocked(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ch := make(chan *Snapshot, 8)
	ch <- initial
	s := &Subscription{C: ch, send: ch, accountID: accountID}
	b.subs[accountID] = append(b.subs[accountID], s)
	return s, nil
}

// buildNowLocked builds a snapshot using a freshly incremented
// version. Caller MUST hold b.mu. Used by Subscribe (race-safety)
// and fire (so version assignment is serialized with Subscribe).
func (b *Bus) buildNowLocked(ctx context.Context, accountID string) (*Snapshot, error) {
	v := b.version.Add(1)
	return b.agg.Build(ctx, accountID, v)
}

// Unsubscribe removes s from the fan-out and closes its channel.
// Idempotent; safe to call multiple times.
func (b *Bus) Unsubscribe(s *Subscription) {
	if s == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	arr := b.subs[s.accountID]
	for i, x := range arr {
		if x == s {
			b.subs[s.accountID] = append(arr[:i], arr[i+1:]...)
			break
		}
	}
	if !s.closed {
		s.closed = true
		close(s.send)
	}
}

// Publish schedules a debounced snapshot recompute + broadcast for
// accountID. Multiple Publish calls within the debounce window
// collapse into one Build. Safe to call from any goroutine; never
// blocks the caller. Safe to call on a nil receiver — test rigs and
// older milestones (M2-M4) that have no state bus configured can
// thread a nil *Bus through every mutation path.
func (b *Bus) Publish(accountID string) {
	if b == nil || accountID == "" {
		return
	}
	b.mu.Lock()
	// If there's no subscriber for this account, skip the work
	// entirely — the next Subscribe will compute an initial
	// snapshot from scratch anyway.
	if len(b.subs[accountID]) == 0 {
		b.mu.Unlock()
		return
	}
	if t, ok := b.pending[accountID]; ok {
		// Reset existing timer.
		if !t.Stop() {
			// Timer already fired; clear it so the fire goroutine's
			// "still pending?" check fails benignly.
			delete(b.pending, accountID)
		} else {
			t.Reset(b.debounce)
			b.mu.Unlock()
			return
		}
	}
	t := time.AfterFunc(b.debounce, func() { b.fire(accountID) })
	b.pending[accountID] = t
	b.mu.Unlock()
}

// PublishMany is a convenience for batch-publishing the same event
// to multiple accounts (e.g., a new message in a room fans out to
// every member's state). Safe to call on a nil receiver.
func (b *Bus) PublishMany(accountIDs []string) {
	if b == nil {
		return
	}
	for _, id := range accountIDs {
		b.Publish(id)
	}
}

// BuildNow synchronously assembles a snapshot for accountID without
// going through the debouncer. Used by GET /v1/state and by
// Subscribe (initial frame).
func (b *Bus) BuildNow(ctx context.Context, accountID string) (*Snapshot, error) {
	v := b.version.Add(1)
	return b.agg.Build(ctx, accountID, v)
}

// Shutdown closes every active subscription and cancels any pending
// debouncer. Used by the daemon's shutdown path so watch handlers
// unblock cleanly.
func (b *Bus) Shutdown() {
	b.mu.Lock()
	for id, t := range b.pending {
		t.Stop()
		delete(b.pending, id)
	}
	for accountID, list := range b.subs {
		for _, s := range list {
			if !s.closed {
				s.closed = true
				close(s.send)
			}
		}
		delete(b.subs, accountID)
	}
	b.mu.Unlock()
}

func (b *Bus) fire(accountID string) {
	// Hold the mutex across BuildNow + fan-out (M5-P3-004 fix). This
	// serializes the atomic.Add for the version counter with
	// Subscribe's buildNowLocked, which means version order matches
	// send order: any subscriber whose initial frame was built under
	// the same mutex will have a lower version, and any frame fire
	// builds after that subscriber registered will have a higher
	// version. Without the mutex held across the BuildNow + send,
	// fire's BuildNow could have observed an earlier version, then
	// raced past Subscribe's initial send on the channel.
	//
	// Performance: BuildNow runs sqlite reads serialized on the
	// single-connection pool anyway, so the global bus mutex doesn't
	// add meaningful contention for M5 throughput.
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, accountID)
	if len(b.subs[accountID]) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := b.buildNowLocked(ctx, accountID)
	if err != nil {
		b.log.Warn("state aggregator failed", "account_id", accountID, "err", err)
		return
	}

	for _, s := range b.subs[accountID] {
		if s.closed {
			continue
		}
		select {
		case s.send <- snap:
		default:
			// Slow consumer — drop. The next Publish will catch them
			// up since BuildNow is always fresh.
		}
	}
}
