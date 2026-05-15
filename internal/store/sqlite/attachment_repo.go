package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// attachmentRepo implements store.AttachmentRepo over SQLite.
//
// Inbound attachments land with empty LocalPath / SHA256 / nil
// DownloadedAt; the downloader fills those in via MarkDownloaded
// once the file is committed to disk. Outbound (send-path) rows
// land with LocalPath = the source path and DownloadedAt = now so
// they show up immediately in `history` listings.
type attachmentRepo struct {
	db queryExecer
}

const attachmentSelectCols = `id, message_id, filename, size, mime, local_path,
       discord_url, downloaded_at, sha256, created_at`

func (r *attachmentRepo) Create(ctx context.Context, a *store.Attachment) error {
	if a == nil {
		return errcode.New(errcode.InvalidArgument, "attachment is nil")
	}
	if a.MessageID == "" {
		return errcode.New(errcode.InvalidArgument, "attachment.MessageID is required")
	}
	if a.Filename == "" {
		return errcode.New(errcode.InvalidArgument, "attachment.Filename is required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO attachments(id, message_id, filename, size, mime, local_path,
                        discord_url, downloaded_at, sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.MessageID, a.Filename, a.Size, a.MIME, a.LocalPath,
		a.DiscordURL, nullableUnix(a.DownloadedAt), a.SHA256, a.CreatedAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "insert attachment")
	}
	return nil
}

func (r *attachmentRepo) Get(ctx context.Context, id string) (*store.Attachment, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+attachmentSelectCols+` FROM attachments WHERE id = ?`, id)
	a, err := scanAttachment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "attachment not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan attachment")
	}
	return a, nil
}

func (r *attachmentRepo) ListByMessage(ctx context.Context, messageID string) ([]*store.Attachment, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+attachmentSelectCols+`
   FROM attachments
  WHERE message_id = ?
  ORDER BY id`, messageID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list attachments by message")
	}
	defer rows.Close()
	return drainAttachmentRows(rows)
}

// ListByMessages issues one query for all the message ids, then
// groups results by message_id. Returns an empty (non-nil) map when
// the input slice is empty.
func (r *attachmentRepo) ListByMessages(ctx context.Context, messageIDs []string) (map[string][]*store.Attachment, error) {
	out := map[string][]*store.Attachment{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(messageIDs))
	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT ` + attachmentSelectCols + `
   FROM attachments
  WHERE message_id IN (` + strings.Join(placeholders, ",") + `)
  ORDER BY message_id, id`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list attachments by messages")
	}
	defer rows.Close()
	atts, err := drainAttachmentRows(rows)
	if err != nil {
		return nil, err
	}
	for _, a := range atts {
		out[a.MessageID] = append(out[a.MessageID], a)
	}
	return out, nil
}

func (r *attachmentRepo) ListPendingDownloads(ctx context.Context, limit int) ([]*store.Attachment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT `+attachmentSelectCols+`
  FROM attachments
 WHERE downloaded_at IS NULL
 ORDER BY created_at ASC, id ASC
 LIMIT ?`, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list pending downloads")
	}
	defer rows.Close()
	return drainAttachmentRows(rows)
}

func (r *attachmentRepo) MarkDownloaded(ctx context.Context, id, localPath, sha256 string, at time.Time) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE attachments
   SET local_path    = ?,
       sha256        = ?,
       downloaded_at = ?
 WHERE id = ?`, localPath, sha256, at.Unix(), id)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "mark attachment downloaded")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "attachment %s not found", id)
	}
	return nil
}

func scanAttachment(row rowScanner) (*store.Attachment, error) {
	var (
		a            store.Attachment
		downloadedAt sql.NullInt64
		createdAt    int64
	)
	if err := row.Scan(&a.ID, &a.MessageID, &a.Filename, &a.Size, &a.MIME, &a.LocalPath,
		&a.DiscordURL, &downloadedAt, &a.SHA256, &createdAt); err != nil {
		return nil, err
	}
	a.DownloadedAt = fromNullableUnix(downloadedAt)
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &a, nil
}

func drainAttachmentRows(rows *sql.Rows) ([]*store.Attachment, error) {
	var out []*store.Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan attachment row")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate attachments")
	}
	return out, nil
}
