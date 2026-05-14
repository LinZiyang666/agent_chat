package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type messageStateRepo struct {
	db queryExecer
}

func (r *messageStateRepo) Upsert(ctx context.Context, s *store.MessageState) error {
	if s == nil {
		return errcode.New(errcode.InvalidArgument, "message state is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO message_states(message_id, account_id, read_at, replied_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(message_id, account_id) DO UPDATE
   SET read_at    = COALESCE(message_states.read_at,    excluded.read_at),
       replied_at = COALESCE(message_states.replied_at, excluded.replied_at)`,
		s.MessageID, s.AccountID, nullableUnix(s.ReadAt), nullableUnix(s.RepliedAt),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "upsert message state")
	}
	return nil
}

func (r *messageStateRepo) Get(ctx context.Context, messageID, accountID string) (*store.MessageState, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT message_id, account_id, read_at, replied_at
  FROM message_states WHERE message_id = ? AND account_id = ?`,
		messageID, accountID)
	var (
		s         store.MessageState
		readAt    sql.NullInt64
		repliedAt sql.NullInt64
	)
	err := row.Scan(&s.MessageID, &s.AccountID, &readAt, &repliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "message state not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan message state")
	}
	s.ReadAt = fromNullableUnix(readAt)
	s.RepliedAt = fromNullableUnix(repliedAt)
	return &s, nil
}

// --- M5 state-aggregator read paths ---------------------------------------

func (r *messageStateRepo) CountUnreadForSubscribed(ctx context.Context, accountID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM message_states ms
  JOIN messages       m  ON m.id = ms.message_id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND ms.read_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0`, accountID).Scan(&n)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count unread for subscribed")
	}
	return n, nil
}

func (r *messageStateRepo) CountMentionsForSubscribed(ctx context.Context, accountID, botUserID string) (int, error) {
	if botUserID == "" {
		return 0, nil
	}
	needle := "%<@" + botUserID + ">%"
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM message_states ms
  JOIN messages       m  ON m.id = ms.message_id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND ms.read_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0
   AND m.content LIKE ?`, accountID, needle).Scan(&n)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count mentions for subscribed")
	}
	return n, nil
}

func (r *messageStateRepo) CountPendingAcksForSubscribed(ctx context.Context, accountID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM message_states ms
  JOIN messages       m  ON m.id = ms.message_id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND m.requires_ack = 1
   AND ms.replied_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0`, accountID).Scan(&n)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count pending acks for subscribed")
	}
	return n, nil
}

func (r *messageStateRepo) CountPriorityForSubscribed(ctx context.Context, accountID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM message_states ms
  JOIN messages       m  ON m.id = ms.message_id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND ms.read_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0
   AND m.priority IN ('urgent','system')`, accountID).Scan(&n)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count priority for subscribed")
	}
	return n, nil
}

func (r *messageStateRepo) UnreadCountByRoomForSubscribed(ctx context.Context, accountID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT m.room_id, COUNT(*)
  FROM message_states ms
  JOIN messages       m  ON m.id = ms.message_id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND ms.read_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0
 GROUP BY m.room_id`, accountID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "unread by room for subscribed")
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var roomID string
		var cnt int
		if err := rows.Scan(&roomID, &cnt); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan unread row")
		}
		out[roomID] = cnt
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate unread rows")
	}
	return out, nil
}

func (r *messageStateRepo) ListMentionsForSubscribed(ctx context.Context, accountID, botUserID string, limit int) ([]*store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if botUserID == "" {
		// No mention token to look for → no mentions.
		return nil, nil
	}
	needle := "%<@" + botUserID + ">%"
	rows, err := r.db.QueryContext(ctx, `
SELECT m.id, m.room_id, m.author_account_id, m.discord_msg_id, m.content,
       m.reply_to_msg_id, m.requires_ack, m.priority, m.created_at, m.content_hash
  FROM messages       m
  JOIN message_states ms ON ms.message_id = m.id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND ms.read_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0
   AND m.content LIKE ?
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT ?`, accountID, needle, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list mentions for subscribed")
	}
	defer rows.Close()
	return drainMessageRows(rows)
}

func (r *messageStateRepo) ListPendingAcksForSubscribed(ctx context.Context, accountID string, limit int) ([]*store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT m.id, m.room_id, m.author_account_id, m.discord_msg_id, m.content,
       m.reply_to_msg_id, m.requires_ack, m.priority, m.created_at, m.content_hash
  FROM messages       m
  JOIN message_states ms ON ms.message_id = m.id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND m.requires_ack = 1
   AND ms.replied_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list pending acks for subscribed")
	}
	defer rows.Close()
	return drainMessageRows(rows)
}

func (r *messageStateRepo) ListPriorityForSubscribed(ctx context.Context, accountID string, limit int) ([]*store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT m.id, m.room_id, m.author_account_id, m.discord_msg_id, m.content,
       m.reply_to_msg_id, m.requires_ack, m.priority, m.created_at, m.content_hash
  FROM messages       m
  JOIN message_states ms ON ms.message_id = m.id
  JOIN memberships    mb ON mb.account_id = ms.account_id AND mb.room_id = m.room_id
  JOIN rooms          rm ON rm.id = m.room_id
 WHERE ms.account_id = ?
   AND ms.read_at IS NULL
   AND mb.subscribed = 1
   AND rm.archived = 0
   AND m.priority IN ('urgent','system')
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list priority for subscribed")
	}
	defer rows.Close()
	return drainMessageRows(rows)
}

func drainMessageRows(rows *sql.Rows) ([]*store.Message, error) {
	var out []*store.Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan message row")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate message rows")
	}
	return out, nil
}

func (r *messageStateRepo) ListByAccount(ctx context.Context, accountID string) ([]*store.MessageState, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT message_id, account_id, read_at, replied_at
  FROM message_states WHERE account_id = ?`, accountID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list message states")
	}
	defer rows.Close()
	var out []*store.MessageState
	for rows.Next() {
		var (
			s         store.MessageState
			readAt    sql.NullInt64
			repliedAt sql.NullInt64
		)
		if err := rows.Scan(&s.MessageID, &s.AccountID, &readAt, &repliedAt); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan message state row")
		}
		s.ReadAt = fromNullableUnix(readAt)
		s.RepliedAt = fromNullableUnix(repliedAt)
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate message states")
	}
	return out, nil
}
