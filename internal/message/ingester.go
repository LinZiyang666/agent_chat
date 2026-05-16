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
	"github.com/LinZiyang666/agentchat/internal/state"
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
	bus     *state.Bus

	mu       sync.Mutex
	attached map[string]*connector.Subscription
}

// New constructs an Ingester. Call AttachAccount each time an account
// successfully goes online. bus may be nil for older test rigs (M3/M4)
// that don't wire the state engine; the ingester's Publish calls
// short-circuit via Bus.Publish's nil-safe check.
func New(conn *connector.Connector, bundler store.Bundler, log *slog.Logger, bus *state.Bus) *Ingester {
	if log == nil {
		log = slog.Default()
	}
	return &Ingester{
		conn:     conn,
		bundler:  bundler,
		log:      log,
		bus:      bus,
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

	// notify is populated inside the tx with the member ids that
	// should receive a state-update publish AFTER the tx commits.
	// Capturing it outside the closure ensures the Publish call
	// only fires on a successful commit (self-audit Finding M-2:
	// the previous version published inside the closure even though
	// the comment claimed "Done AFTER the tx"; this aligns code and
	// comment, and avoids scheduling rebuilds for rolled-back txs).
	var notify []string

	if err := i.bundler.WithTx(ctx, func(b store.Bundle) error {
		notify = nil // reset across retries (if any future bundler retries)
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
			// M9: inbound message carries Discord-native mention flags.
			// MentionEveryone goes on the messages row; per-account
			// mentions are written below into message_mentions after
			// we know the persisted id and the bot_user_id -> account_id
			// mapping is resolved.
			MentionEveryone: e.Message.MentionEveryone,
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

		// Resolve the room's current member set once. Used for:
		//   1. fan-out of message_states (inserted path).
		//   2. M9 Phase 1: filtering mention targets to current
		//      members only — Discord's `mentions` array can include
		//      users who aren't in the agentchat room (e.g. someone
		//      we no longer share the channel with), and recording
		//      message_mentions for non-members would pollute the
		//      mention feed of accounts the aggregator otherwise
		//      shields with its memberships join.
		members, err := b.Memberships.ListByRoom(ctx, room.ID)
		if err != nil {
			return err
		}
		memberSet := make(map[string]struct{}, len(members))
		for _, m := range members {
			memberSet[m.AccountID] = struct{}{}
		}

		// M9 Phase 1: persist mention metadata on BOTH the inserted
		// and the conflict path. Why both:
		//
		// - The conflict path runs when the send handler raced ahead
		//   and wrote the messages row first. The gateway echo
		//   carries the authoritative Discord-side mention list,
		//   which the send-path's req.MentionAll mirror can't see.
		//   Skipping the echo (the M9 Phase 1 review's P2-1 finding)
		//   would drop that data.
		//
		// - The inserted path is the common case where the message
		//   originated externally; the same code populates
		//   message_mentions / mention_everyone from the inbound
		//   event.
		//
		// Both writes are idempotent: MergeMentionEveryone is
		// OR-merge (true sticks, false is no-op), AddForMessage is
		// INSERT OR IGNORE keyed on (message_id, account_id). So
		// reordering the two paths cannot lose data.
		mentionMetadataChanged := false
		if e.Message.MentionEveryone {
			if err := b.Messages.MergeMentionEveryone(ctx, persistedID, true); err != nil {
				return err
			}
			mentionMetadataChanged = true
		}
		var mentionedAccountIDs []string
		if len(e.Message.MentionedBotUserIDs) > 0 {
			mentionedAccountIDs, err = i.resolveMentions(
				ctx, b, e.Message.MentionedBotUserIDs, memberSet)
			if err != nil {
				return err
			}
			if len(mentionedAccountIDs) > 0 {
				if err := b.MessageMentions.AddForMessage(
					ctx, persistedID, mentionedAccountIDs); err != nil {
					return err
				}
				mentionMetadataChanged = true
			}
		}

		if !inserted {
			// Outbound echo of a message the send handler already
			// persisted. message_states + attachments were written
			// there too; only mention metadata above was new. Notify
			// all current members so the aggregator picks the
			// updated mention rows up — otherwise totals.mentions
			// would lag until the next unrelated event.
			if mentionMetadataChanged {
				ids := make([]string, 0, len(members))
				for _, m := range members {
					ids = append(ids, m.AccountID)
				}
				notify = ids
			}
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
		nowPtr := func() *time.Time { t := time.Now().UTC(); return &t }
		ids := make([]string, 0, len(members))
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
			ids = append(ids, m.AccountID)
		}

		// M7: index any attachments the gateway reported for this
		// message. local_path / sha256 stay empty + downloaded_at
		// NULL — the downloader picks these rows up via
		// AttachmentRepo.ListPendingDownloads, fetches the bytes,
		// and calls MarkDownloaded when the file lands on disk.
		// Same-tx insertion guarantees that observers reading
		// attachments via /v1/rooms/{id}/messages right after the
		// state-bus publish see the placeholder rows (with empty
		// local_path) rather than nothing at all.
		for _, att := range e.Message.Attachments {
			attID, err := uuid.NewV7()
			if err != nil {
				return errcode.Wrap(err, errcode.Internal, "uuidv7 for attachment")
			}
			row := &store.Attachment{
				ID:         attID.String(),
				MessageID:  persistedID,
				Filename:   att.Filename,
				Size:       att.Size,
				MIME:       att.MIME,
				DiscordURL: att.URL,
				CreatedAt:  time.Now().UTC(),
			}
			if err := b.Attachments.Create(ctx, row); err != nil {
				return err
			}
		}

		notify = ids
		return nil
	}); err != nil {
		return err
	}

	// M5 state fan-out — runs ONLY after a successful tx commit so
	// watchers never see a rebuild triggered by a rolled-back write.
	i.bus.PublishMany(notify)
	return nil
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

// resolveMentions maps each Discord user snowflake in botUserIDs to an
// agentchat account_id. A bot_user_id is kept only if:
//   - it matches an agentchat account (external Discord humans drop
//     out — they don't have a state inbox here), AND
//   - that account is a current member of the room (membership filter
//     enforced via memberSet; non-members must not end up in
//     message_mentions because the aggregator's joins would otherwise
//     fail to shield them from a targeted ping that crossed rooms).
//
// Order in the returned slice is unspecified; duplicates are
// deduplicated. Returns an empty slice (not nil) when no mention maps
// to a known room member.
//
// One List+linear-scan per inbound message is acceptable because the
// number of agentchat accounts is small (Discord caps individual
// developers at ~10 applications, see architecture D3.1) and the per-
// message mention list is also bounded.
func (i *Ingester) resolveMentions(ctx context.Context, b store.Bundle, botUserIDs []string, memberSet map[string]struct{}) ([]string, error) {
	if len(botUserIDs) == 0 {
		return nil, nil
	}
	accounts, err := b.Accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	byBotID := make(map[string]string, len(accounts))
	for _, a := range accounts {
		if a.BotUserID != "" {
			byBotID[a.BotUserID] = a.ID
		}
	}
	seen := make(map[string]struct{}, len(botUserIDs))
	out := make([]string, 0, len(botUserIDs))
	for _, bid := range botUserIDs {
		aid, ok := byBotID[bid]
		if !ok {
			continue
		}
		if _, isMember := memberSet[aid]; !isMember {
			continue
		}
		if _, dup := seen[aid]; dup {
			continue
		}
		seen[aid] = struct{}{}
		out = append(out, aid)
	}
	return out, nil
}
