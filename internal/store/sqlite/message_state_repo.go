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
