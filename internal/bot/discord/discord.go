// Package discord is the Discord-backed implementation of bot.Provider.
// It wraps github.com/bwmarrin/discordgo and translates Discord-shaped
// events / responses into the normalized bot.* types.
//
// Construction (New) does no I/O; Connect opens the gateway and
// returns after the Ready handshake completes. Disconnect cleanly
// shuts down the session and closes the events channel.
//
// Intents requested:
//   - Guilds, GuildMembers           (channel/member metadata)
//   - GuildMessages, MessageContent  (privileged; needed to see content)
//
// The operator must enable the privileged intents in the Discord
// Developer Portal for each bot application; without MessageContent
// the gateway will deliver MessageCreate events with empty Content.
package discord

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Options customizes Provider construction.
type Options struct {
	// GuildID is the Discord server (guild) the bot operates in.
	// Required for CreateChannel; optional for other methods. Defaults
	// to "" — M3 demo flows do not need it.
	GuildID string
	// ReadyTimeout is how long Connect waits for the Ready handshake
	// after Open. Zero means 30 seconds.
	ReadyTimeout time.Duration
}

// Provider is the Discord-backed bot.Provider.
type Provider struct {
	token string
	opts  Options

	mu       sync.Mutex
	session  *discordgo.Session
	identity bot.Identity
	status   bot.ConnStatus
	events   chan bot.Event
	closed   bool

	ready     chan struct{}
	readyOnce sync.Once
}

// Compile-time interface check.
var _ bot.Provider = (*Provider)(nil)

// New builds a Provider. The hint Identity is used as a fallback if
// Ready hasn't fired yet; once Connect completes, Identity is replaced
// with what Discord reports.
func New(token string, hint bot.Identity, opts Options) *Provider {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 30 * time.Second
	}
	return &Provider{
		token:    token,
		opts:     opts,
		identity: hint,
		status:   bot.StatusOffline,
		events:   make(chan bot.Event, 64),
		ready:    make(chan struct{}),
	}
}

// Connect opens the discordgo session, registers our handlers, and
// waits for Ready (or context cancellation, or ReadyTimeout).
func (p *Provider) Connect(ctx context.Context) error {
	p.mu.Lock()
	if p.session != nil {
		p.mu.Unlock()
		return errcode.New(errcode.Conflict, "provider already connected")
	}
	p.status = bot.StatusConnecting
	p.mu.Unlock()

	s, err := discordgo.New("Bot " + p.token)
	if err != nil {
		p.setStatus(bot.StatusErrored)
		return errcode.Wrap(err, errcode.AuthInvalid, "construct discord session")
	}
	s.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMembers |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	s.AddHandler(p.handleReady)
	s.AddHandler(p.handleMessageCreate)
	s.AddHandler(p.handleDisconnect)
	s.AddHandler(p.handleChannelDelete)

	if err := s.Open(); err != nil {
		p.setStatus(bot.StatusErrored)
		return errcode.Wrap(err, errcode.Unavailable, "open discord session")
	}

	p.mu.Lock()
	p.session = s
	p.mu.Unlock()

	select {
	case <-p.ready:
		p.setStatus(bot.StatusOnline)
		return nil
	case <-ctx.Done():
		_ = s.Close()
		p.clearSession()
		return errcode.Wrap(ctx.Err(), errcode.Unavailable, "discord ready wait cancelled")
	case <-time.After(p.opts.ReadyTimeout):
		_ = s.Close()
		p.clearSession()
		return errcode.New(errcode.Unavailable, "discord ready handshake timed out")
	}
}

// Disconnect closes the discordgo session and the Events() channel.
// Safe to call when never connected (no-op) or twice (second is no-op).
//
// Ordering (M3-P3-004 audit guidance): the final EventDisconnected
// is delivered BEFORE the events channel is closed, so a consumer
// using `for ev := range p.Events()` observes the disconnect event
// then the channel close. The publish and the close run under one
// critical section so any concurrent handler / publish path
// serializes against the close.
func (p *Provider) Disconnect(_ context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	s := p.session
	if s == nil {
		// Never connected; close the unused events channel and mark
		// closed under the same critical section.
		p.closed = true
		close(p.events)
		p.mu.Unlock()
		return nil
	}
	p.session = nil
	p.status = bot.StatusOffline
	p.mu.Unlock()

	closeErr := s.Close()

	p.mu.Lock()
	if !p.closed {
		// Best-effort publish of the final EventDisconnected before
		// closing the channel. A full buffer means the consumer is
		// behind; we still close so for-range exits.
		select {
		case p.events <- bot.EventDisconnected{Reason: "disconnect"}:
		default:
		}
		p.closed = true
		close(p.events)
	}
	p.mu.Unlock()

	if closeErr != nil {
		return errcode.Wrap(closeErr, errcode.Internal, "close discord session")
	}
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

func (p *Provider) Events() <-chan bot.Event {
	return p.events
}

// SendMessage delivers content to channelID. opts.ReplyToMessageID, if
// set, makes the message a Discord reply. opts.Attachments, if
// non-empty, uploads the files alongside the message via
// ChannelMessageSendComplex.
//
// Each upload file is opened, its bytes streamed to Discord, and the
// caller's *os.File closed when SendMessage returns. The CLI / API
// layer is responsible for the Discord per-file size check (free
// tier 10 MB as of 2024-09; boosted guilds higher); if the
// aggregate exceeds the limit, Discord rejects with HTTP 413 which
// surfaces as Unavailable.
func (p *Provider) SendMessage(_ context.Context, channelID, content string, opts bot.SendOptions) (*bot.Message, error) {
	s, err := p.requireSession()
	if err != nil {
		return nil, err
	}

	// M9 Phase 2: one Complex-path build for every send. The old
	// split (ChannelMessageSend / ChannelMessageSendReply for the
	// fast path) couldn't carry AllowedMentions cleanly, and the
	// Reply helper internally devolves to a Complex call anyway —
	// see discordgo/restapi.go:1829. Doing it ourselves lets
	// AllowedMentions, attachments, and Reference share a single
	// codepath.
	send := &discordgo.MessageSend{
		Content: content,
		AllowedMentions: &discordgo.MessageAllowedMentions{
			// Default-deny: only IDs the daemon parser explicitly
			// resolved get pinged. A stray `@everyone` literal in
			// content with MentionAllowedEveryone=false renders but
			// does not page anyone.
			Parse: []discordgo.AllowedMentionType{},
			Users: opts.MentionAllowedUserIDs,
			// RepliedUser mirrors the Discord client default: a
			// reply pings the original author. The reply-as-ping
			// behaviour is what humans expect; the AllowedMentions
			// declaration is explicit so the default-deny on Parse
			// can't accidentally suppress it.
			RepliedUser: opts.ReplyToMessageID != "",
		},
	}
	if opts.MentionAllowedEveryone {
		send.AllowedMentions.Parse = append(send.AllowedMentions.Parse,
			discordgo.AllowedMentionTypeEveryone)
	}
	if opts.ReplyToMessageID != "" {
		send.Reference = &discordgo.MessageReference{
			MessageID: opts.ReplyToMessageID,
			ChannelID: channelID,
		}
	}

	// Attachments: open here, close after the Complex call returns.
	// Open errors surface as InvalidArgument (the caller's path is
	// suspect); upload errors as Unavailable (Discord is the
	// authority).
	if len(opts.Attachments) > 0 {
		files := make([]*discordgo.File, 0, len(opts.Attachments))
		openHandles := make([]*os.File, 0, len(opts.Attachments))
		defer func() {
			for _, h := range openHandles {
				_ = h.Close()
			}
		}()
		for _, a := range opts.Attachments {
			f, err := os.Open(a.Path)
			if err != nil {
				return nil, errcode.Wrap(err, errcode.InvalidArgument,
					"open attachment %s", a.Path)
			}
			openHandles = append(openHandles, f)
			name := a.FileName
			if name == "" {
				name = filepath.Base(a.Path)
			}
			files = append(files, &discordgo.File{
				Name:        name,
				ContentType: a.MIME,
				Reader:      f,
			})
		}
		send.Files = files
	}

	msg, e := s.ChannelMessageSendComplex(channelID, send)
	if e != nil {
		return nil, classifyChannelError(e, "send", channelID)
	}
	return discordToMessage(msg), nil
}

// classifyChannelError maps a Discord REST error onto the agentchat
// errcode space. Most failures are transient (rate limit, gateway
// hiccup) so the default is UNAVAILABLE; two cases we lift out:
//
//   - 10003 "Unknown Channel" / HTTP 404 → NotFound. The channel was
//     deleted out-of-band in Discord; callers can rely on this code to
//     stop retrying and react (e.g. archive the matching room).
//   - HTTP 403 → PermDenied. Bot lost permission on the channel (role
//     removed, override flipped); retrying won't help either.
//
// op is a short verb ("send", "delete", "create") used in the wrapping
// message so the caller's log line is self-descriptive.
func classifyChannelError(e error, op, channelID string) error {
	var rerr *discordgo.RESTError
	if errors.As(e, &rerr) {
		if rerr.Message != nil && rerr.Message.Code == discordgo.ErrCodeUnknownChannel {
			return errcode.Wrap(e, errcode.NotFound,
				"discord channel %s no longer exists", channelID)
		}
		if rerr.Response != nil {
			switch rerr.Response.StatusCode {
			case 404:
				return errcode.Wrap(e, errcode.NotFound,
					"discord channel %s no longer exists", channelID)
			case 403:
				return errcode.Wrap(e, errcode.PermDenied,
					"discord rejected %s on channel %s (forbidden)", op, channelID)
			}
		}
	}
	return errcode.Wrap(e, errcode.Unavailable, "%s discord channel", op)
}

// CreateChannel creates a guild text channel. Requires GuildID via
// Options; returns InvalidArgument if not configured.
func (p *Provider) CreateChannel(_ context.Context, name string) (string, error) {
	if p.opts.GuildID == "" {
		return "", errcode.New(errcode.InvalidArgument,
			"discord adapter has no guild_id configured")
	}
	s, err := p.requireSession()
	if err != nil {
		return "", err
	}
	ch, e := s.GuildChannelCreate(p.opts.GuildID, name, discordgo.ChannelTypeGuildText)
	if e != nil {
		return "", errcode.Wrap(e, errcode.Unavailable, "create discord channel")
	}
	return ch.ID, nil
}

// DeleteChannel removes a channel. A 404 / Unknown Channel surfaces
// as errcode.NotFound so the caller can treat "already gone" as a
// success state for the room-delete flow.
func (p *Provider) DeleteChannel(_ context.Context, channelID string) error {
	s, err := p.requireSession()
	if err != nil {
		return err
	}
	if _, e := s.ChannelDelete(channelID); e != nil {
		return classifyChannelError(e, "delete", channelID)
	}
	return nil
}

// AddMember grants VIEW_CHANNEL on channelID to memberID via a
// per-channel permission override.
func (p *Provider) AddMember(_ context.Context, channelID, memberID string) error {
	s, err := p.requireSession()
	if err != nil {
		return err
	}
	if e := s.ChannelPermissionSet(channelID, memberID,
		discordgo.PermissionOverwriteTypeMember,
		discordgo.PermissionViewChannel, 0); e != nil {
		return errcode.Wrap(e, errcode.Unavailable, "grant channel permission")
	}
	return nil
}

// RemoveMember deletes the per-channel override for memberID.
func (p *Provider) RemoveMember(_ context.Context, channelID, memberID string) error {
	s, err := p.requireSession()
	if err != nil {
		return err
	}
	if e := s.ChannelPermissionDelete(channelID, memberID); e != nil {
		return errcode.Wrap(e, errcode.Unavailable, "remove channel permission")
	}
	return nil
}

// FetchHistory returns up to limit messages older than beforeID,
// oldest-first per the Provider contract.
func (p *Provider) FetchHistory(_ context.Context, channelID, beforeID string, limit int) ([]bot.Message, error) {
	s, err := p.requireSession()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	raw, e := s.ChannelMessages(channelID, limit, beforeID, "", "")
	if e != nil {
		return nil, errcode.Wrap(e, errcode.Unavailable, "fetch discord history")
	}
	// discordgo returns newest-first; reverse to oldest-first.
	out := make([]bot.Message, 0, len(raw))
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i].Author == nil {
			continue
		}
		out = append(out, *discordToMessage(raw[i]))
	}
	return out, nil
}

// --- discordgo event handlers ---

func (p *Provider) handleReady(_ *discordgo.Session, r *discordgo.Ready) {
	p.mu.Lock()
	if r.User != nil {
		p.identity = bot.Identity{
			UserID:   r.User.ID,
			Username: r.User.Username,
		}
		if r.User.Avatar != "" {
			p.identity.AvatarURL = r.User.AvatarURL("")
		}
	}
	id := p.identity
	p.mu.Unlock()

	p.unsafePublish(bot.EventConnected{Identity: id})
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *Provider) handleMessageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil || m.Author == nil {
		return
	}
	p.mu.Lock()
	self := p.identity.UserID
	p.mu.Unlock()
	// Skip our own messages — the SendMessage caller already received
	// the published form synchronously.
	if self != "" && m.Author.ID == self {
		return
	}
	p.unsafePublish(bot.EventMessageNew{Message: *discordToMessage(m.Message)})
}

func (p *Provider) handleDisconnect(_ *discordgo.Session, _ *discordgo.Disconnect) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.status = bot.StatusErrored
	p.mu.Unlock()
	p.unsafePublish(bot.EventDisconnected{Reason: "gateway closed"})
}

// handleChannelDelete forwards Discord's out-of-band channel delete
// to the ingester via the normalized bot.EventChannelDeleted. The
// downstream ingester archives the matching agentchat room so later
// reads / sends report a CONFLICT instead of silently 404-ing.
//
// Every bot in the guild that observes the channel gets this event
// from Discord; the ingester is idempotent (archiving an already
// archived room is a no-op).
func (p *Provider) handleChannelDelete(_ *discordgo.Session, c *discordgo.ChannelDelete) {
	if c == nil || c.Channel == nil || c.Channel.ID == "" {
		return
	}
	p.unsafePublish(bot.EventChannelDeleted{ChannelID: c.Channel.ID})
}

// --- helpers ---

func (p *Provider) requireSession() (*discordgo.Session, error) {
	p.mu.Lock()
	s := p.session
	p.mu.Unlock()
	if s == nil {
		return nil, errcode.New(errcode.Unavailable, "discord provider is not connected")
	}
	return s, nil
}

func (p *Provider) setStatus(s bot.ConnStatus) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

func (p *Provider) clearSession() {
	p.mu.Lock()
	p.session = nil
	p.status = bot.StatusErrored
	p.mu.Unlock()
}

// unsafePublish enqueues an event without blocking; drops if the
// channel is full or already closed.
//
// The mutex is held across the send so a concurrent Disconnect
// (which closes the channel under the same mutex) cannot race past
// us and cause a send-on-closed panic. The previous unlock-then-send
// pattern was the M3-P3-004 race the audit caught.
func (p *Provider) unsafePublish(e bot.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.events <- e:
	default:
		// Drop — slow consumer.
	}
}

// discordToMessage normalizes a discordgo.Message into our bot.Message.
func discordToMessage(m *discordgo.Message) *bot.Message {
	if m == nil {
		return nil
	}
	var author string
	if m.Author != nil {
		author = m.Author.ID
	}
	out := &bot.Message{
		ID:              m.ID,
		ChannelID:       m.ChannelID,
		AuthorID:        author,
		Content:         m.Content,
		CreatedAt:       m.Timestamp,
		MentionEveryone: m.MentionEveryone,
	}
	// M9: capture per-user mentions. Each entry is a Discord user
	// snowflake; the daemon ingester maps these to accountchat
	// account IDs through accounts.bot_user_id. We don't attempt
	// any mapping here — provider stays platform-shaped.
	if len(m.Mentions) > 0 {
		out.MentionedBotUserIDs = make([]string, 0, len(m.Mentions))
		for _, u := range m.Mentions {
			if u == nil || u.ID == "" {
				continue
			}
			out.MentionedBotUserIDs = append(out.MentionedBotUserIDs, u.ID)
		}
	}
	if len(m.Attachments) > 0 {
		out.Attachments = make([]bot.MsgAttachURL, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			if a == nil {
				continue
			}
			out.Attachments = append(out.Attachments, bot.MsgAttachURL{
				Filename: a.Filename,
				URL:      a.URL,
				Size:     int64(a.Size),
				MIME:     a.ContentType,
			})
		}
	}
	return out
}
