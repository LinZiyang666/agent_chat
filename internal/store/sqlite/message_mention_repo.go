package sqlite

import (
	"context"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// messageMentionRepo persists rows of message_mentions
// (per-message @-mention set). Introduced by M9 Phase 1 alongside the
// new state.CountMentionsForSubscribed predicate.
type messageMentionRepo struct {
	db queryExecer
}

// SetForMessage replaces the mention set for messageID with accountIDs.
// The two-step delete+insert keeps the call idempotent without
// requiring a UNIQUE INDEX dance. Prefer AddForMessage when adding
// rows resolved from a single inbound event — Set wipes the table
// first, which loses races against the send path.
func (r *messageMentionRepo) SetForMessage(ctx context.Context, messageID string, accountIDs []string) error {
	if messageID == "" {
		return errcode.New(errcode.InvalidArgument, "messageID is empty")
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM message_mentions WHERE message_id = ?`, messageID); err != nil {
		return errcode.Wrap(err, errcode.Internal, "delete message_mentions")
	}
	return r.insertAll(ctx, messageID, accountIDs)
}

// AddForMessage unions accountIDs into the existing mention set. INSERT
// OR IGNORE handles the (message_id, account_id) PK so re-inserting an
// already-present row is a no-op rather than a constraint error. The
// ingester uses this in BOTH the inserted and conflict paths so a
// gateway echo never wipes mentions that the send handler may have
// already resolved (M9 Phase 2 onwards).
func (r *messageMentionRepo) AddForMessage(ctx context.Context, messageID string, accountIDs []string) error {
	if messageID == "" {
		return errcode.New(errcode.InvalidArgument, "messageID is empty")
	}
	return r.insertAll(ctx, messageID, accountIDs)
}

// insertAll is the shared INSERT OR IGNORE loop used by both Set (after
// the wipe) and Add. Volume is bounded by Discord's per-message mention
// cap; we are inside the caller's WithTx so SQLite batches at storage.
func (r *messageMentionRepo) insertAll(ctx context.Context, messageID string, accountIDs []string) error {
	if len(accountIDs) == 0 {
		return nil
	}
	for _, aid := range accountIDs {
		if aid == "" {
			continue
		}
		if _, err := r.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO message_mentions(message_id, account_id) VALUES (?, ?)`,
			messageID, aid); err != nil {
			return errcode.Wrap(err, errcode.Internal, "insert message_mentions")
		}
	}
	return nil
}

// ListForMessage returns the account IDs mentioned in messageID.
func (r *messageMentionRepo) ListForMessage(ctx context.Context, messageID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT account_id FROM message_mentions WHERE message_id = ?`, messageID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list message_mentions")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan message_mentions")
		}
		out = append(out, aid)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "rows err message_mentions")
	}
	return out, nil
}
