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
       reply_to_msg_id, priority, created_at, content_hash, mention_everyone`

func (r *messageRepo) Create(ctx context.Context, m *store.Message) error {
	if m == nil {
		return errcode.New(errcode.InvalidArgument, "message is nil")
	}
	if !m.Priority.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid priority %q", m.Priority)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO messages(id, room_id, author_account_id, discord_msg_id, content,
                     reply_to_msg_id, priority, created_at, content_hash,
                     mention_everyone)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RoomID, nullableString(m.AuthorAccountID), m.DiscordMsgID, m.Content,
		nullableString(m.ReplyToMsgID), string(m.Priority),
		m.CreatedAt.Unix(), m.ContentHash, boolToInt(m.MentionEveryone),
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
                     reply_to_msg_id, priority, created_at, content_hash,
                     mention_everyone)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(discord_msg_id) DO NOTHING`,
		m.ID, m.RoomID, nullableString(m.AuthorAccountID), m.DiscordMsgID, m.Content,
		nullableString(m.ReplyToMsgID), string(m.Priority),
		m.CreatedAt.Unix(), m.ContentHash, boolToInt(m.MentionEveryone),
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

// LatestPerRoomForMember returns the latest message in each room the
// account is a member of (subscribed or not). Used by the M5
// aggregator's recently-active dimension.
//
// Self-audit Finding M-1 fix: the previous implementation used
// `(room_id, MAX(created_at), MAX(id)) IN (...)` which evaluates
// the two MAX aggregates independently — under same-millisecond
// ties or any case where the row with MAX(id) is not the row with
// MAX(created_at), the tuple wouldn't match any real row and the
// room would silently drop out of the result. We now use a window
// function (ROW_NUMBER OVER (PARTITION BY room_id ORDER BY ...))
// which picks one concrete row per partition deterministically.
func (r *messageRepo) LatestPerRoomForMember(ctx context.Context, accountID string) (map[string]*store.Message, error) {
	rows, err := r.db.QueryContext(ctx, `
WITH ranked AS (
  SELECT m.id, m.room_id, m.author_account_id, m.discord_msg_id, m.content,
         m.reply_to_msg_id, m.priority, m.created_at, m.content_hash,
         m.mention_everyone,
         ROW_NUMBER() OVER (PARTITION BY m.room_id ORDER BY m.created_at DESC, m.id DESC) AS rn
    FROM messages m
    JOIN memberships mb ON mb.room_id = m.room_id
    JOIN rooms       rm ON rm.id = m.room_id
   WHERE mb.account_id = ?
     AND rm.archived = 0
)
SELECT id, room_id, author_account_id, discord_msg_id, content,
       reply_to_msg_id, priority, created_at, content_hash, mention_everyone
  FROM ranked
 WHERE rn = 1`, accountID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "latest per room for member")
	}
	defer rows.Close()
	out := map[string]*store.Message{}
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan latest row")
		}
		out[m.RoomID] = m
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate latest rows")
	}
	return out, nil
}

// ApplySendMetadata writes the send-path-owned columns onto an existing
// row. Used by the M4-P3-010 race fix in SendMessage.
func (r *messageRepo) ApplySendMetadata(ctx context.Context, id string, m store.SendMetadata) error {
	if !m.Priority.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid priority %q", m.Priority)
	}
	// M9 Phase 2: send-owned columns shrank — requires_ack /
	// mention_all are no longer written from this path. They remain
	// in the schema until the Phase 2 schema migration drops them.
	// mention_everyone is OR-merged (MAX) so the ingester's
	// gateway-echo observation is never clobbered by a later
	// send-side write of false.
	res, err := r.db.ExecContext(ctx, `
UPDATE messages
   SET author_account_id = ?,
       reply_to_msg_id   = ?,
       priority          = ?,
       content_hash      = ?,
       mention_everyone  = MAX(mention_everyone, ?)
 WHERE id = ?`,
		nullableString(m.AuthorAccountID),
		nullableString(m.ReplyToMsgID),
		string(m.Priority),
		m.ContentHash,
		boolToInt(m.MentionEveryone),
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

// MergeMentionEveryone OR-merges flag into messages.mention_everyone.
// Called by the ingester's conflict path (M9 Phase 1) so an echoed
// `@everyone=true` survives a prior or subsequent send-side write of
// false. NotFound if the row is missing — surface to caller so it can
// decide whether the conflict id was real.
func (r *messageRepo) MergeMentionEveryone(ctx context.Context, id string, flag bool) error {
	if !flag {
		// Nothing to merge: `MAX(existing, 0) = existing`, so the
		// UPDATE would be a no-op write. Skip the disk hit entirely.
		// Still verify the row exists so the caller's contract
		// (NotFound when id is wrong) is preserved.
		var dummy int
		err := r.db.QueryRowContext(ctx,
			`SELECT 1 FROM messages WHERE id = ?`, id).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return errcode.New(errcode.NotFound, "message %s not found", id)
		}
		if err != nil {
			return errcode.Wrap(err, errcode.Internal, "check message existence")
		}
		return nil
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE messages SET mention_everyone = 1 WHERE id = ?`, id)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "merge mention_everyone")
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

// ListUnreadForAccountInRoom returns messages in roomID where the
// caller's per-account state row has read_at IS NULL. The join to
// message_states is what restricts the visibility to rooms where the
// account actually has an inbox (fan-out only writes states for
// current members at send time; joining a room after the fact does NOT
// retroactively create rows for older messages).
func (r *messageRepo) ListUnreadForAccountInRoom(ctx context.Context, accountID, roomID string, limit int) ([]*store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT m.id, m.room_id, m.author_account_id, m.discord_msg_id, m.content,
       m.reply_to_msg_id, m.priority, m.created_at, m.content_hash,
       m.mention_everyone
  FROM messages       m
  JOIN message_states ms ON ms.message_id = m.id
 WHERE m.room_id     = ?
   AND ms.account_id = ?
   AND ms.read_at IS NULL
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT ?`, roomID, accountID, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list unread for account in room")
	}
	defer rows.Close()
	var out []*store.Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan unread row")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate unread rows")
	}
	return out, nil
}

// ListReadHistoryForAccountInRoom returns messages in roomID where
// the caller's per-account state row has read_at IS NOT NULL,
// newest-first up to limit. Used as the "context" block that the M9
// read-room handler appends to the unread feed.
func (r *messageRepo) ListReadHistoryForAccountInRoom(ctx context.Context, accountID, roomID string, limit int) ([]*store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT m.id, m.room_id, m.author_account_id, m.discord_msg_id, m.content,
       m.reply_to_msg_id, m.priority, m.created_at, m.content_hash,
       m.mention_everyone
  FROM messages       m
  JOIN message_states ms ON ms.message_id = m.id
 WHERE m.room_id     = ?
   AND ms.account_id = ?
   AND ms.read_at IS NOT NULL
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT ?`, roomID, accountID, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list read history for account in room")
	}
	defer rows.Close()
	var out []*store.Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan read history row")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate read history rows")
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessageRow(row rowScanner) (*store.Message, error) {
	var (
		m               store.Message
		authorID        sql.NullString
		replyToID       sql.NullString
		priority        string
		createdAt       int64
		mentionEveryone int
	)
	err := row.Scan(&m.ID, &m.RoomID, &authorID, &m.DiscordMsgID, &m.Content,
		&replyToID, &priority, &createdAt, &m.ContentHash, &mentionEveryone)
	if err != nil {
		return nil, err
	}
	m.AuthorAccountID = fromNullableString(authorID)
	m.ReplyToMsgID = fromNullableString(replyToID)
	m.Priority = store.MessagePriority(priority)
	m.CreatedAt = time.Unix(createdAt, 0).UTC()
	m.MentionEveryone = mentionEveryone != 0
	return &m, nil
}
