package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// announcementRepo implements store.AnnouncementRepo over SQLite.
//
// Versions are assigned monotonically per room: Create calls
// NextVersion(roomID) inside the caller's transaction (when WithTx is
// used) so concurrent posters cannot collide. Without a transaction
// we still rely on the single-writer SQLite connection (MaxOpenConns=1)
// to serialize Create attempts; under heavier concurrency the caller
// should wrap Create in WithTx.
type announcementRepo struct {
	db queryExecer
}

const announcementSelectCols = `id, room_id, content, version, created_by, created_at`

func (r *announcementRepo) Create(ctx context.Context, a *store.Announcement) error {
	if a == nil {
		return errcode.New(errcode.InvalidArgument, "announcement is nil")
	}
	if a.RoomID == "" {
		return errcode.New(errcode.InvalidArgument, "announcement.RoomID is required")
	}
	if a.Content == "" {
		return errcode.New(errcode.InvalidArgument, "announcement.Content is required")
	}
	if a.Version <= 0 {
		return errcode.New(errcode.InvalidArgument,
			"announcement.Version must be >= 1 (caller passes NextVersion)")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO announcements(id, room_id, content, version, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.RoomID, a.Content, a.Version, nullableString(a.CreatedBy), a.CreatedAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "insert announcement")
	}
	return nil
}

func (r *announcementRepo) Get(ctx context.Context, id string) (*store.Announcement, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+announcementSelectCols+` FROM announcements WHERE id = ?`, id)
	a, err := scanAnnouncement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "announcement not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan announcement")
	}
	return a, nil
}

func (r *announcementRepo) Latest(ctx context.Context, roomID string) (*store.Announcement, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT `+announcementSelectCols+`
  FROM announcements
 WHERE room_id = ?
 ORDER BY version DESC, id DESC
 LIMIT 1`, roomID)
	a, err := scanAnnouncement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "no announcement for room %s", roomID)
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan latest announcement")
	}
	return a, nil
}

func (r *announcementRepo) NextVersion(ctx context.Context, roomID string) (int, error) {
	var v sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM announcements WHERE room_id = ?`, roomID).Scan(&v)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "next announcement version")
	}
	if !v.Valid {
		return 1, nil
	}
	return int(v.Int64) + 1, nil
}

func scanAnnouncement(row rowScanner) (*store.Announcement, error) {
	var (
		a         store.Announcement
		createdBy sql.NullString
		createdAt int64
	)
	if err := row.Scan(&a.ID, &a.RoomID, &a.Content, &a.Version, &createdBy, &createdAt); err != nil {
		return nil, err
	}
	// CreatedBy becomes the empty string if the creator account was
	// deleted (FK SET NULL). Callers that need to distinguish "never
	// recorded" from "creator deleted" can compare against the
	// audit_log; the public DTO surfaces "" either way.
	a.CreatedBy = fromNullableString(createdBy)
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &a, nil
}

// --- AnnouncementReadRepo -------------------------------------------------

type announcementReadRepo struct {
	db queryExecer
}

func (r *announcementReadRepo) Upsert(ctx context.Context, rr *store.AnnouncementRead) error {
	if rr == nil {
		return errcode.New(errcode.InvalidArgument, "announcement read is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO announcement_reads(announcement_id, account_id, read_at)
VALUES (?, ?, ?)
ON CONFLICT(announcement_id, account_id) DO UPDATE
   SET read_at = excluded.read_at`,
		rr.AnnouncementID, rr.AccountID, rr.ReadAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "upsert announcement read")
	}
	return nil
}

func (r *announcementReadRepo) IsRead(ctx context.Context, announcementID, accountID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `
SELECT 1 FROM announcement_reads WHERE announcement_id = ? AND account_id = ?`,
		announcementID, accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errcode.Wrap(err, errcode.Internal, "is announcement read")
	}
	return true, nil
}

// CountUnreadForAccount counts announcements in rooms the account is a
// member of (any subscription state, any archived state) where the
// account has not yet posted an ack row. Only the highest-version row
// per room counts toward unread — superseded versions are implicitly
// "old news" and not surfaced.
func (r *announcementReadRepo) CountUnreadForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
WITH latest AS (
  SELECT a.id, a.room_id
    FROM announcements a
   WHERE (a.room_id, a.version) IN (
       SELECT room_id, MAX(version) FROM announcements GROUP BY room_id
   )
)
SELECT COUNT(*)
  FROM latest l
  JOIN memberships mb ON mb.room_id = l.room_id AND mb.account_id = ?
  LEFT JOIN announcement_reads ar
         ON ar.announcement_id = l.id AND ar.account_id = ?
 WHERE ar.account_id IS NULL`, accountID, accountID).Scan(&n)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count unread announcements")
	}
	return n, nil
}

// ListUnreadForAccount returns the same set as CountUnreadForAccount,
// joined with the announcement row, newest-first (by created_at, id).
// Limit defaults to 50, capped at 200.
func (r *announcementReadRepo) ListUnreadForAccount(ctx context.Context, accountID string, limit int) ([]*store.Announcement, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
WITH latest AS (
  SELECT a.id, a.room_id, a.content, a.version, a.created_by, a.created_at
    FROM announcements a
   WHERE (a.room_id, a.version) IN (
       SELECT room_id, MAX(version) FROM announcements GROUP BY room_id
   )
)
SELECT l.id, l.room_id, l.content, l.version, l.created_by, l.created_at
  FROM latest l
  JOIN memberships mb ON mb.room_id = l.room_id AND mb.account_id = ?
  LEFT JOIN announcement_reads ar
         ON ar.announcement_id = l.id AND ar.account_id = ?
 WHERE ar.account_id IS NULL
 ORDER BY l.created_at DESC, l.id DESC
 LIMIT ?`, accountID, accountID, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list unread announcements")
	}
	defer rows.Close()
	var out []*store.Announcement
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan unread announcement")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate unread announcements")
	}
	return out, nil
}
