package connector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

type blockingProvider struct {
	entered chan<- struct{}
	release <-chan struct{}
	events  chan bot.Event
}

func (p *blockingProvider) Connect(ctx context.Context) error {
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *blockingProvider) Disconnect(context.Context) error {
	close(p.events)
	return nil
}

func (p *blockingProvider) Status() bot.ConnStatus { return bot.StatusOnline }
func (p *blockingProvider) Identity() bot.Identity { return bot.Identity{} }
func (p *blockingProvider) SendMessage(context.Context, string, string, bot.SendOptions) (*bot.Message, error) {
	return nil, nil
}
func (p *blockingProvider) CreateChannel(context.Context, string) (string, error) { return "", nil }
func (p *blockingProvider) DeleteChannel(context.Context, string) error           { return nil }
func (p *blockingProvider) AddMember(context.Context, string, string) error       { return nil }
func (p *blockingProvider) RemoveMember(context.Context, string, string) error    { return nil }
func (p *blockingProvider) FetchHistory(context.Context, string, string, int) ([]bot.Message, error) {
	return nil, nil
}
func (p *blockingProvider) Events() <-chan bot.Event { return p.events }

func TestConcurrentConnectSameAccountAllowsOnlyOneProvider(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var (
		mu        sync.Mutex
		providers []*blockingProvider
	)
	c := New(func(_ string, _ bot.Identity) bot.Provider {
		p := &blockingProvider{
			entered: entered,
			release: release,
			events:  make(chan bot.Event),
		}
		mu.Lock()
		providers = append(providers, p)
		mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	errCh := make(chan error, 2)
	go func() { errCh <- c.Connect(context.Background(), "same", "tok", bot.Identity{}) }()

	require.Eventually(t, func() bool {
		return len(entered) == 1
	}, time.Second, 10*time.Millisecond)
	go func() { errCh <- c.Connect(context.Background(), "same", "tok", bot.Identity{}) }()

	require.Eventually(t, func() bool {
		return len(entered) == 2 || len(errCh) > 0
	}, time.Second, 10*time.Millisecond)
	close(release)

	err1, err2 := <-errCh, <-errCh
	successes := 0
	conflicts := 0
	for _, err := range []error{err1, err2} {
		if err == nil {
			successes++
			continue
		}
		e, _ := errcode.As(err)
		if e != nil && e.Code == errcode.Conflict {
			conflicts++
		}
	}
	assert.Equal(t, 1, successes, "only one concurrent Connect may succeed")
	assert.Equal(t, 1, conflicts, "the other concurrent Connect must return Conflict")

	mu.Lock()
	for _, p := range providers {
		close(p.events)
	}
	mu.Unlock()
}

func TestDisconnectErrorKeepsProviderRegistered(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "a", "t", bot.Identity{Username: "x"}))
	p := fx.latest()
	p.DisconnectErr = errors.New("provider still connected")

	err := fx.c.Disconnect(context.Background(), "a")
	require.Error(t, err)

	got, ok := fx.c.Provider("a")
	require.True(t, ok, "failed disconnect must keep provider reachable for retry")
	assert.Same(t, p, got)
	assert.True(t, fx.c.IsRegistered("a"))
	assert.Equal(t, bot.StatusOnline, fx.c.Status("a"))
}
