package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// systemAnnouncementRepo implements store.SystemAnnouncementRepo over
// SQLite. There is no per-room scope and no versioning; each
// announcement is independent and every account is implicitly unread
// until an ack row lands.
type systemAnnouncementRepo struct {
	db queryExecer
}

const systemAnnSelectCols = `id, content, created_by, created_at`

func (r *systemAnnouncementRepo) Create(ctx context.Context, a *store.SystemAnnouncement) error {
	if a == nil {
		return errcode.New(errcode.InvalidArgument, "system announcement is nil")
	}
	if a.Content == "" {
		return errcode.New(errcode.InvalidArgument, "system announcement content is required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO system_announcements(id, content, created_by, created_at)
VALUES (?, ?, ?, ?)`,
		a.ID, a.Content, nullableString(a.CreatedBy), a.CreatedAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "insert system announcement")
	}
	return nil
}

func (r *systemAnnouncementRepo) Get(ctx context.Context, id string) (*store.SystemAnnouncement, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+systemAnnSelectCols+` FROM system_announcements WHERE id = ?`, id)
	a, err := scanSystemAnn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "system announcement not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan system announcement")
	}
	return a, nil
}

func (r *systemAnnouncementRepo) List(ctx context.Context, limit int) ([]*store.SystemAnnouncement, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT `+systemAnnSelectCols+`
  FROM system_announcements
 ORDER BY created_at DESC, id DESC
 LIMIT ?`, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list system announcements")
	}
	defer rows.Close()
	var out []*store.SystemAnnouncement
	for rows.Next() {
		a, err := scanSystemAnn(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan system announcement row")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate system announcements")
	}
	return out, nil
}

func scanSystemAnn(row rowScanner) (*store.SystemAnnouncement, error) {
	var (
		a         store.SystemAnnouncement
		createdBy sql.NullString
		createdAt int64
	)
	if err := row.Scan(&a.ID, &a.Content, &createdBy, &createdAt); err != nil {
		return nil, err
	}
	// CreatedBy becomes the empty string if the admin account was
	// deleted (FK SET NULL); same M6-P3-002 fix as announcements.
	a.CreatedBy = fromNullableString(createdBy)
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &a, nil
}

// --- SystemAnnouncementReadRepo -------------------------------------------

type systemAnnouncementReadRepo struct {
	db queryExecer
}

func (r *systemAnnouncementReadRepo) Upsert(ctx context.Context, rr *store.SystemAnnouncementRead) error {
	if rr == nil {
		return errcode.New(errcode.InvalidArgument, "system announcement read is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO system_announcement_reads(sys_ann_id, account_id, read_at)
VALUES (?, ?, ?)
ON CONFLICT(sys_ann_id, account_id) DO UPDATE
   SET read_at = excluded.read_at`,
		rr.SysAnnID, rr.AccountID, rr.ReadAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "upsert system announcement read")
	}
	return nil
}

func (r *systemAnnouncementReadRepo) IsRead(ctx context.Context, sysAnnID, accountID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `
SELECT 1 FROM system_announcement_reads WHERE sys_ann_id = ? AND account_id = ?`,
		sysAnnID, accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errcode.Wrap(err, errcode.Internal, "is system announcement read")
	}
	return true, nil
}

func (r *systemAnnouncementReadRepo) CountUnreadForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM system_announcements sa
  LEFT JOIN system_announcement_reads sar
         ON sar.sys_ann_id = sa.id AND sar.account_id = ?
 WHERE sar.account_id IS NULL`, accountID).Scan(&n)
	if err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count unread system announcements")
	}
	return n, nil
}

func (r *systemAnnouncementReadRepo) ListUnreadForAccount(ctx context.Context, accountID string, limit int) ([]*store.SystemAnnouncement, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT sa.id, sa.content, sa.created_by, sa.created_at
  FROM system_announcements sa
  LEFT JOIN system_announcement_reads sar
         ON sar.sys_ann_id = sa.id AND sar.account_id = ?
 WHERE sar.account_id IS NULL
 ORDER BY sa.created_at DESC, sa.id DESC
 LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list unread system announcements")
	}
	defer rows.Close()
	var out []*store.SystemAnnouncement
	for rows.Next() {
		a, err := scanSystemAnn(rows)
		if err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan unread system ann")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate unread system anns")
	}
	return out, nil
}
