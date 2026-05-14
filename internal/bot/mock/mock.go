// Package mock provides an in-memory bot.Provider for tests. Tests can
// drive event delivery (InjectEvent, InjectMessage), inspect side
// effects (Sent, Created, Deleted, …), and override behavior (set the
// public *Err fields to make a method return that error).
package mock

import (
	"context"
	"sync"
	"time"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Provider is the test double. The zero value is unusable; build one
// via New.
type Provider struct {
	mu       sync.Mutex
	status   bot.ConnStatus
	identity bot.Identity
	events   chan bot.Event
	closed   bool

	// Side-effect ledgers — exposed for assertions.
	Sent          []bot.Message
	Created       []string
	Deleted       []string
	Added         [][2]string // (channelID, memberID)
	Removed       [][2]string
	History       map[string][]bot.Message
	NextMessageID int

	// Programmable error injectors.
	ConnectErr       error
	DisconnectErr    error
	SendErr          error
	CreateChannelErr error
	DeleteChannelErr error
	AddMemberErr     error
	RemoveMemberErr  error
	HistoryErr       error
}

// New constructs a Provider with the given starting identity. The
// status is StatusOffline until Connect is called.
func New(identity bot.Identity) *Provider {
	return &Provider{
		status:   bot.StatusOffline,
		identity: identity,
		events:   make(chan bot.Event, 64),
		History:  map[string][]bot.Message{},
	}
}

func (p *Provider) Connect(_ context.Context) error {
	if p.ConnectErr != nil {
		return p.ConnectErr
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errcode.New(errcode.Conflict, "provider already disconnected")
	}
	p.status = bot.StatusOnline
	p.mu.Unlock()
	p.publish(bot.EventConnected{Identity: p.identity})
	return nil
}

func (p *Provider) Disconnect(_ context.Context) error {
	if p.DisconnectErr != nil {
		return p.DisconnectErr
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.status = bot.StatusOffline
	p.closed = true
	p.mu.Unlock()
	p.publish(bot.EventDisconnected{Reason: "disconnect called"})
	close(p.events)
	return nil
}

func (p *Provider) Status() bot.ConnStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *Provider) Identity() bot.Identity {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identity
}

func (p *Provider) SendMessage(_ context.Context, channelID, content string, opts bot.SendOptions) (*bot.Message, error) {
	if p.SendErr != nil {
		return nil, p.SendErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.NextMessageID++
	m := bot.Message{
		ID:        idFor(p.NextMessageID),
		ChannelID: channelID,
		AuthorID:  p.identity.UserID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	_ = opts // M3 mock ignores reply targets; assert on Sent slice if needed
	p.Sent = append(p.Sent, m)
	return &m, nil
}

func (p *Provider) CreateChannel(_ context.Context, name string) (string, error) {
	if p.CreateChannelErr != nil {
		return "", p.CreateChannelErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	id := "ch-" + name
	p.Created = append(p.Created, id)
	return id, nil
}

func (p *Provider) DeleteChannel(_ context.Context, channelID string) error {
	if p.DeleteChannelErr != nil {
		return p.DeleteChannelErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Deleted = append(p.Deleted, channelID)
	return nil
}

func (p *Provider) AddMember(_ context.Context, channelID, memberID string) error {
	if p.AddMemberErr != nil {
		return p.AddMemberErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Added = append(p.Added, [2]string{channelID, memberID})
	return nil
}

func (p *Provider) RemoveMember(_ context.Context, channelID, memberID string) error {
	if p.RemoveMemberErr != nil {
		return p.RemoveMemberErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Removed = append(p.Removed, [2]string{channelID, memberID})
	return nil
}

func (p *Provider) FetchHistory(_ context.Context, channelID, _ string, _ int) ([]bot.Message, error) {
	if p.HistoryErr != nil {
		return nil, p.HistoryErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bot.Message(nil), p.History[channelID]...), nil
}

func (p *Provider) Events() <-chan bot.Event {
	return p.events
}

// InjectMessage publishes an EventMessageNew on the events channel. No-op
// if the provider has been disconnected.
func (p *Provider) InjectMessage(m bot.Message) {
	p.publish(bot.EventMessageNew{Message: m})
}

// InjectEvent publishes an arbitrary Event on the channel.
func (p *Provider) InjectEvent(e bot.Event) {
	p.publish(e)
}

func (p *Provider) publish(e bot.Event) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	// Non-blocking publish: drop on full buffer rather than deadlocking
	// the test. Tests with a tight events channel can drain via Events.
	select {
	case p.events <- e:
	default:
	}
}

// idFor renders a deterministic message id for tests.
func idFor(n int) string {
	return "msg-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
