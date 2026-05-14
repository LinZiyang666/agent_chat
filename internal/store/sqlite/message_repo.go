package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type messageRepo struct {
	db queryExecer
}

const messageSelectCols = `id, room_id, author_account_id, discord_msg_id, content,
       reply_to_msg_id, requires_ack, priority, created_at, content_hash`

func (r *messageRepo) Create(ctx context.Context, m *store.Message) error {
	if m == nil {
		return errcode.New(errcode.InvalidArgument, "message is nil")
	}
	if !m.Priority.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid priority %q", m.Priority)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO messages(id, room_id, author_account_id, discord_msg_id, content,
                     reply_to_msg_id, requires_ack, priority, created_at, content_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RoomID, nullableString(m.AuthorAccountID), m.DiscordMsgID, m.Content,
		nullableString(m.ReplyToMsgID), boolToInt(m.RequiresAck), string(m.Priority),
		m.CreatedAt.Unix(), m.ContentHash,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.New(errcode.Conflict,
				"message with discord_msg_id %q already exists", m.DiscordMsgID)
		}
		return errcode.Wrap(err, errcode.Internal, "insert message")
	}
	return nil
}

func (r *messageRepo) CreateIgnoreConflict(ctx context.Context, m *store.Message) (string, bool, error) {
	if m == nil {
		return "", false, errcode.New(errcode.InvalidArgument, "message is nil")
	}
	if !m.Priority.Valid() {
		return "", false, errcode.New(errcode.InvalidArgument, "invalid priority %q", m.Priority)
	}
	res, err := r.db.ExecContext(ctx, `
INSERT INTO messages(id, room_id, author_account_id, discord_msg_id, content,
                     reply_to_msg_id, requires_ack, priority, created_at, content_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(discord_msg_id) DO NOTHING`,
		m.ID, m.RoomID, nullableString(m.AuthorAccountID), m.DiscordMsgID, m.Content,
		nullableString(m.ReplyToMsgID), boolToInt(m.RequiresAck), string(m.Priority),
		m.CreatedAt.Unix(), m.ContentHash,
	)
	if err != nil {
		return "", false, errcode.Wrap(err, errcode.Internal, "insert-or-ignore message")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 1 {
		return m.ID, true, nil
	}
	// Conflict path: fetch the existing row's id.
	row := r.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE discord_msg_id = ?`, m.DiscordMsgID)
	var existingID string
	if err := row.Scan(&existingID); err != nil {
		return "", false, errcode.Wrap(err, errcode.Internal, "lookup conflicting message id")
	}
	return existingID, false, nil
}

func (r *messageRepo) Get(ctx context.Context, id string) (*store.Message, error) {
	return r.fetchOne(ctx,
		`SELECT `+messageSelectCols+` FROM messages WHERE id = ?`, id)
}

func (r *messageRepo) GetByDiscordMsgID(ctx context.Context, discordMsgID string) (*store.Message, error) {
	return r.fetchOne(ctx,
		`SELECT `+messageSelectCols+` FROM messages WHERE discord_msg_id = ?`, discordMsgID)
}

func (r *messageRepo) fetchOne(ctx context.Context, query, arg string) (*store.Message, error) {
	row := r.db.QueryRowContext(ctx, query, arg)
	m, err := scanMessageRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "message not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan message")
	}
	return m, nil
}

func (r *messageRepo) List(ctx context.Context, f store.MessageFilter) ([]*store.Message, error) {
	if f.RoomID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "MessageFilter.RoomID is required")
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	// Tie-break on (created_at DESC, id DESC) so same-second rows
	// stay deterministic (UUIDv7 ids are time-ordered, like the
	// M2-P3-013 audit list).
	if f.BeforeID == "" {
		rows, err = r.db.QueryContext(ctx, `
SELECT `+messageSelectCols+`
  FROM messages
 WHERE room_id = ?
 ORDER BY created_at DESC, id DESC
 LIMIT ?`, f.RoomID, limit)
	} else {
		rows, err = r.db.QueryContext(ctx, `
SELECT `+messageSelectCols+`
  FROM messages
 WHERE room_id = ?
   AND (created_at, id) <
       (SELECT created_at, id FROM messages WHERE id = ?)
 ORDER BY created_at DESC, id DESC
 LIMIT ?`, f.RoomID, f.BeforeID, limit)
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list messages")
	}
	defer rows.Close()
	var out []*store.Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan message row")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate messages")
	}
	return out, nil
}

// ApplySendMetadata writes the send-path-owned columns onto an existing
// row. Used by the M4-P3-010 race fix in SendMessage.
func (r *messageRepo) ApplySendMetadata(ctx context.Context, id string, m store.SendMetadata) error {
	if !m.Priority.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid priority %q", m.Priority)
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE messages
   SET author_account_id = ?,
       reply_to_msg_id   = ?,
       requires_ack      = ?,
       priority          = ?,
       content_hash      = ?
 WHERE id = ?`,
		nullableString(m.AuthorAccountID),
		nullableString(m.ReplyToMsgID),
		boolToInt(m.RequiresAck),
		string(m.Priority),
		m.ContentHash,
		id,
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "apply send metadata")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "message %s not found", id)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessageRow(row rowScanner) (*store.Message, error) {
	var (
		m           store.Message
		authorID    sql.NullString
		replyToID   sql.NullString
		requiresAck int
		priority    string
		createdAt   int64
	)
	err := row.Scan(&m.ID, &m.RoomID, &authorID, &m.DiscordMsgID, &m.Content,
		&replyToID, &requiresAck, &priority, &createdAt, &m.ContentHash)
	if err != nil {
		return nil, err
	}
	m.AuthorAccountID = fromNullableString(authorID)
	m.ReplyToMsgID = fromNullableString(replyToID)
	m.RequiresAck = requiresAck != 0
	m.Priority = store.MessagePriority(priority)
	m.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &m, nil
}
