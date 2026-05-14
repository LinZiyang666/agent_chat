package connector

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/bot/mock"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

type fixture struct {
	t   *testing.T
	c   *Connector
	mu  sync.Mutex
	all []*mock.Provider
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fx := &fixture{t: t}
	fx.c = New(func(token string, hint bot.Identity) bot.Provider {
		p := mock.New(bot.Identity{UserID: "u-" + hint.Username, Username: hint.Username})
		fx.mu.Lock()
		fx.all = append(fx.all, p)
		fx.mu.Unlock()
		return p
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return fx
}

func (f *fixture) latest() *mock.Provider {
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(f.t, f.all)
	return f.all[len(f.all)-1]
}

func TestConnectHappy(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "a1", "tok", bot.Identity{Username: "alice"}))
	assert.Equal(t, bot.StatusOnline, fx.c.Status("a1"))
	p, ok := fx.c.Provider("a1")
	require.True(t, ok)
	assert.Equal(t, "alice", p.Identity().Username)
}

func TestConnectTwiceConflict(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "a", "t", bot.Identity{Username: "x"}))
	err := fx.c.Connect(context.Background(), "a", "t", bot.Identity{Username: "x"})
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestDisconnectHappy(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "a", "t", bot.Identity{}))
	require.NoError(t, fx.c.Disconnect(context.Background(), "a"))
	assert.Equal(t, bot.StatusOffline, fx.c.Status("a"))
}

func TestDisconnectNotConnected(t *testing.T) {
	fx := newFixture(t)
	err := fx.c.Disconnect(context.Background(), "ghost")
	require.Error(t, err)
	e, _ := errcode.As(err)
	assert.Equal(t, errcode.Conflict, e.Code)
}

func TestSubscribeReceivesEvents(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "w", "t", bot.Identity{Username: "watcher"}))
	sub := fx.c.Subscribe("w")
	defer fx.c.Unsubscribe(sub)

	// First event after connect is EventConnected (from mock.Connect).
	select {
	case ev := <-sub.C:
		_, ok := ev.(bot.EventConnected)
		assert.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("never saw connect event")
	}

	fx.latest().InjectMessage(bot.Message{ID: "m1", ChannelID: "ch", AuthorID: "u-x", Content: "hello"})
	select {
	case ev := <-sub.C:
		mn, ok := ev.(bot.EventMessageNew)
		require.True(t, ok)
		assert.Equal(t, "hello", mn.Message.Content)
	case <-time.After(time.Second):
		t.Fatal("never saw injected message")
	}
}

func TestSubscribeClosesOnDisconnect(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "x", "t", bot.Identity{}))
	sub := fx.c.Subscribe("x")
	<-sub.C // drain connect event
	require.NoError(t, fx.c.Disconnect(context.Background(), "x"))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, ok := <-sub.C
		if !ok {
			return // closed — pass
		}
	}
	t.Fatal("subscription never closed after disconnect")
}

func TestShutdownDisconnectsAll(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.c.Connect(context.Background(), "a", "t", bot.Identity{}))
	require.NoError(t, fx.c.Connect(context.Background(), "b", "t", bot.Identity{}))
	fx.c.Shutdown(context.Background())
	assert.Equal(t, bot.StatusOffline, fx.c.Status("a"))
	assert.Equal(t, bot.StatusOffline, fx.c.Status("b"))
}
