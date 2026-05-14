// Package message contains the inbound-message ingester (M4): a
// background loop per online account that drains the Connector event
// stream and persists messages + per-subscriber message_states inside
// bundler.WithTx, so the persisted state stays consistent (M2-P3-012
// architectural pattern).
//
// The future M5 state-aggregator will consume from this DB; this
// package only handles the write side.
package message

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// Ingester wires Connector subscriptions to the message DB. There is
// one background goroutine per attached account; each goroutine exits
// when the underlying Connector subscription channel closes (which
// happens naturally on Disconnect — see internal/connector pump tail).
type Ingester struct {
	conn    *connector.Connector
	bundler store.Bundler
	log     *slog.Logger

	mu       sync.Mutex
	attached map[string]*connector.Subscription
}

// New constructs an Ingester. Call AttachAccount each time an account
// successfully goes online.
func New(conn *connector.Connector, bundler store.Bundler, log *slog.Logger) *Ingester {
	if log == nil {
		log = slog.Default()
	}
	return &Ingester{
		conn:     conn,
		bundler:  bundler,
		log:      log,
		attached: map[string]*connector.Subscription{},
	}
}

// AttachAccount subscribes to accountID's event stream and starts the
// drain goroutine. Safe to call concurrently; a second call for the
// same account is a no-op (a duplicate subscription would just create
// orphan goroutines, not corruption).
func (i *Ingester) AttachAccount(accountID string) {
	i.mu.Lock()
	if _, ok := i.attached[accountID]; ok {
		i.mu.Unlock()
		return
	}
	sub := i.conn.Subscribe(accountID)
	i.attached[accountID] = sub
	i.mu.Unlock()

	go i.run(accountID, sub)
}

// DetachAccount stops the drain goroutine for accountID (if any) by
// unsubscribing — which closes the Subscription's channel under the
// Connector's mutex, so the run loop exits.
func (i *Ingester) DetachAccount(accountID string) {
	i.mu.Lock()
	sub, ok := i.attached[accountID]
	if ok {
		delete(i.attached, accountID)
	}
	i.mu.Unlock()
	if sub != nil {
		i.conn.Unsubscribe(sub)
	}
}

// IsAttached reports whether accountID currently has a drain
// goroutine running. Exposed for tests.
func (i *Ingester) IsAttached(accountID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.attached[accountID]
	return ok
}

func (i *Ingester) run(accountID string, sub *connector.Subscription) {
	defer func() {
		i.mu.Lock()
		delete(i.attached, accountID)
		i.mu.Unlock()
	}()
	for ev := range sub.C {
		i.dispatch(accountID, ev)
	}
}

func (i *Ingester) dispatch(accountID string, ev bot.Event) {
	switch e := ev.(type) {
	case bot.EventMessageNew:
		if err := i.ingestNew(accountID, e); err != nil {
			i.log.Warn("ingest message_new failed",
				"account_id", accountID,
				"discord_msg_id", e.Message.ID,
				"err", err)
		}
	default:
		// Other event types (EventConnected, EventDisconnected, etc.)
		// are not persisted by the ingester. The Connector's subscription
		// fan-out still delivers them to /v1/debug/events consumers; we
		// just drop them here.
	}
}

// ingestNew is the per-event critical section. The persistence (room
// lookup → message INSERT-or-IGNORE → per-subscriber message_states)
// runs inside a single bundler.WithTx so the message row and its
// fanned-out state rows commit atomically.
func (i *Ingester) ingestNew(ingesterAccountID string, e bot.EventMessageNew) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return i.bundler.WithTx(ctx, func(b store.Bundle) error {
		room, err := b.Rooms.GetByDiscordChannelID(ctx, e.Message.ChannelID)
		if err != nil {
			if ec, _ := errcode.As(err); ec != nil && ec.Code == errcode.NotFound {
				// Message arrived for a channel that isn't catalogued
				// as a room. We drop it silently — this happens on
				// channels the bot can see but we haven't mapped.
				return nil
			}
			return err
		}

		// Resolve Discord author_id to an agentchat account_id, when
		// possible. M4 stores this mapping on accounts.bot_user_id.
		// A NULL author is fine — messages from external Discord
		// users still get persisted for history.
		authorAccountID, err := i.resolveAuthor(ctx, b, e.Message.AuthorID)
		if err != nil {
			return err
		}

		newID, err := uuid.NewV7()
		if err != nil {
			return errcode.Wrap(err, errcode.Internal, "uuidv7")
		}
		hash := sha256.Sum256([]byte(e.Message.Content))

		msg := &store.Message{
			ID:              newID.String(),
			RoomID:          room.ID,
			AuthorAccountID: authorAccountID,
			DiscordMsgID:    e.Message.ID,
			Content:         e.Message.Content,
			Priority:        store.PriorityNormal,
			CreatedAt:       e.Message.CreatedAt.UTC(),
			ContentHash:     hex.EncodeToString(hash[:]),
		}
		// If the send-path already wrote the row (because we sent the
		// message ourselves via POST /v1/rooms/{id}/messages and the
		// gateway echoed it back), CreateIgnoreConflict short-circuits
		// and returns the existing id. We use that id when fanning
		// out message_states below.
		persistedID, inserted, err := b.Messages.CreateIgnoreConflict(ctx, msg)
		if err != nil {
			return err
		}
		if !inserted {
			// Outbound echo of a message we already persisted via the
			// send handler: states were created at send time. Skip.
			return nil
		}

		// Fan out one message_states row per CURRENT MEMBER of the
		// room — including unsubscribed (旁观) members. Per
		// requirements §4 and §5.1 subscription only governs primary
		// vs secondary state UI in M5; it does NOT govern whether the
		// row exists (M4-P3-005 fix).
		//
		// read_at = NULL for everyone except the author (if the
		// author is one of our accounts, send counts as read).
		members, err := b.Memberships.ListByRoom(ctx, room.ID)
		if err != nil {
			return err
		}
		nowPtr := func() *time.Time { t := time.Now().UTC(); return &t }
		for _, m := range members {
			st := &store.MessageState{
				MessageID: persistedID,
				AccountID: m.AccountID,
			}
			if authorAccountID != "" && m.AccountID == authorAccountID {
				st.ReadAt = nowPtr()
			}
			if err := b.MessageStates.Upsert(ctx, st); err != nil {
				return err
			}
		}
		return nil
	})
}

// resolveAuthor looks up the bot account whose bot_user_id matches
// discordUserID. Returns "" if no agentchat account owns that bot.
func (i *Ingester) resolveAuthor(ctx context.Context, b store.Bundle, discordUserID string) (string, error) {
	if discordUserID == "" {
		return "", nil
	}
	accounts, err := b.Accounts.List(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.BotUserID == discordUserID {
			return a.ID, nil
		}
	}
	return "", nil
}
