package state

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// newBusFx builds a fresh sqlite-backed Aggregator and a Bus with a
// small debounce window so tests don't have to wait 200ms.
func newBusFx(t *testing.T) (*fx, *Bus) {
	t.Helper()
	f := newFx(t)
	bus := NewBusWithDebounce(f.agg, slog.New(slog.NewTextHandler(io.Discard, nil)), 30*time.Millisecond)
	t.Cleanup(bus.Shutdown)
	return f, bus
}

func TestBusBuildNowIncrementsVersion(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	s1, err := bus.BuildNow(context.Background(), viewer.ID)
	require.NoError(t, err)
	s2, err := bus.BuildNow(context.Background(), viewer.ID)
	require.NoError(t, err)
	assert.Greater(t, s2.Version, s1.Version, "version is monotonic")
}

func TestBusSubscribeReceivesInitialSnapshot(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	sub, err := bus.Subscribe(context.Background(), viewer.ID)
	require.NoError(t, err)
	defer bus.Unsubscribe(sub)

	select {
	case s := <-sub.C:
		require.NotNil(t, s)
		assert.Equal(t, viewer.ID, s.AccountID)
	case <-time.After(time.Second):
		t.Fatal("expected initial snapshot")
	}
}

func TestBusPublishDebouncesBurst(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	sub, err := bus.Subscribe(context.Background(), viewer.ID)
	require.NoError(t, err)
	defer bus.Unsubscribe(sub)
	<-sub.C // drain initial frame

	// Fire 10 publishes in quick succession; with a 30ms debounce
	// we should receive exactly one extra snapshot.
	for i := 0; i < 10; i++ {
		bus.Publish(viewer.ID)
		time.Sleep(2 * time.Millisecond)
	}

	got := 0
	deadline := time.After(200 * time.Millisecond)
loop:
	for {
		select {
		case _, ok := <-sub.C:
			if !ok {
				break loop
			}
			got++
		case <-deadline:
			break loop
		}
	}
	assert.Equal(t, 1, got, "10 quick publishes must collapse to 1 debounced frame")
}

func TestBusPublishNoSubscribersIsCheap(t *testing.T) {
	_, bus := newBusFx(t)
	// No subscribers — Publish should be a no-op.
	for i := 0; i < 1000; i++ {
		bus.Publish("phantom")
	}
}

func TestBusUnsubscribeClosesChannel(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	sub, err := bus.Subscribe(context.Background(), viewer.ID)
	require.NoError(t, err)
	<-sub.C // drain initial

	bus.Unsubscribe(sub)
	// Second Unsubscribe is a no-op.
	bus.Unsubscribe(sub)

	// The channel is now closed.
	_, open := <-sub.C
	assert.False(t, open)
}

func TestBusShutdownClosesAllSubscriptions(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	subs := []*Subscription{}
	for i := 0; i < 5; i++ {
		s, err := bus.Subscribe(context.Background(), viewer.ID)
		require.NoError(t, err)
		<-s.C
		subs = append(subs, s)
	}
	bus.Shutdown()
	for _, s := range subs {
		_, open := <-s.C
		assert.False(t, open, "Shutdown must close every subscription channel")
	}
}

func TestBusNilSafePublish(t *testing.T) {
	var bus *Bus
	bus.Publish("anyone")          // must not panic
	bus.PublishMany([]string{"x"}) // must not panic
}

func TestBusConcurrentPublishSubscribeUnsubscribeNoDeadlock(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Subscriber churn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s, err := bus.Subscribe(context.Background(), viewer.ID)
				if err == nil {
					go func(s *Subscription) {
						// Drain until close.
						for range s.C {
						}
					}(s)
					time.Sleep(5 * time.Millisecond)
					bus.Unsubscribe(s)
				}
			}
		}
	}()

	// Publish churn.
	var publishes atomic.Int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					bus.Publish(viewer.ID)
					publishes.Add(1)
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	assert.Greater(t, publishes.Load(), int64(0))
}

func TestBusIdleNoSends(t *testing.T) {
	f, bus := newBusFx(t)
	viewer := f.account("v", "u-v")
	sub, err := bus.Subscribe(context.Background(), viewer.ID)
	require.NoError(t, err)
	defer bus.Unsubscribe(sub)
	<-sub.C // drain initial

	// No publish + nothing changes → no new frames.
	select {
	case s, ok := <-sub.C:
		t.Fatalf("idle bus should not emit; got snapshot=%v ok=%v", s, ok)
	case <-time.After(100 * time.Millisecond):
	}
}

// silence unused-import warnings on bot in some Go versions.
var _ = bot.StatusOffline

// TestBusPublishDuringSubscribeInitialBuildDeliversFollowUp pins the
// M5-P3-004 fix deterministically: a Publish that arrives while
// Subscribe is still building the initial snapshot must wait until
// the subscriber is registered, then schedule a follow-up frame.
func TestBusPublishDuringSubscribeInitialBuildDeliversFollowUp(t *testing.T) {
	f := newFx(t)
	viewer := f.account("v", "u-v")

	base := f.store.Bundle()
	started := make(chan struct{})
	release := make(chan struct{})
	base.Accounts = &blockingAccountRepo{
		AccountRepo: base.Accounts,
		accountID:   viewer.ID,
		started:     started,
		release:     release,
	}
	agg := New(base, func(string) bot.ConnStatus { return bot.StatusOnline })
	bus := NewBusWithDebounce(agg, slog.New(slog.NewTextHandler(io.Discard, nil)), 30*time.Millisecond)
	t.Cleanup(bus.Shutdown)

	subCh := make(chan *Subscription, 1)
	errCh := make(chan error, 1)
	go func() {
		sub, err := bus.Subscribe(context.Background(), viewer.ID)
		if err != nil {
			errCh <- err
			return
		}
		subCh <- sub
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Subscribe never entered the initial snapshot build")
	}

	published := make(chan struct{})
	go func() {
		bus.Publish(viewer.ID)
		close(published)
	}()

	select {
	case <-published:
		t.Fatal("Publish returned before Subscribe registered the subscriber")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)

	var sub *Subscription
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case sub = <-subCh:
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not return")
	}
	defer bus.Unsubscribe(sub)

	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("Publish did not return after Subscribe completed")
	}
	select {
	case first := <-sub.C:
		require.NotNil(t, first)
		assert.Equal(t, viewer.ID, first.AccountID)
	case <-time.After(time.Second):
		t.Fatal("expected initial frame")
	}
	select {
	case next := <-sub.C:
		require.NotNil(t, next)
		assert.Equal(t, viewer.ID, next.AccountID)
	case <-time.After(time.Second):
		t.Fatal("expected follow-up frame for Publish racing with Subscribe")
	}
}

type blockingAccountRepo struct {
	store.AccountRepo
	accountID string
	started   chan<- struct{}
	release   <-chan struct{}
	once      sync.Once
}

func (r *blockingAccountRepo) Get(ctx context.Context, id string) (*store.Account, error) {
	if id == r.accountID {
		r.once.Do(func() { close(r.started) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.AccountRepo.Get(ctx, id)
}
