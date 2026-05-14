package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type auditRepo struct {
	db queryExecer
}

// defaultAuditLimit caps List results when the caller passes Limit=0.
// Audit queries are usually scoped (by account or time), but we still
// want a safety net to avoid accidentally pulling the whole table.
const defaultAuditLimit = 500

func (r *auditRepo) Record(ctx context.Context, e *store.AuditEntry) error {
	if e == nil {
		return errcode.New(errcode.InvalidArgument, "audit entry is nil")
	}
	if e.Action == "" {
		return errcode.New(errcode.InvalidArgument, "audit action is empty")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO audit_log(id, account_id, action, target, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.AccountID, e.Action,
		nullableString(e.Target), nullableString(e.Payload),
		e.CreatedAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "insert audit entry")
	}
	return nil
}

func (r *auditRepo) List(ctx context.Context, f store.AuditFilter) ([]*store.AuditEntry, error) {
	var (
		where []string
		args  []any
	)
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Since != nil {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since.Unix())
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultAuditLimit
	}

	q := `SELECT id, account_id, action, target, payload, created_at FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// Secondary sort by id DESC: when two entries share the same
	// created_at (truncated to seconds), the lexicographically larger
	// UUIDv7 wins. UUIDv7 is time-ordered, so this matches "newer first"
	// even at sub-second granularity.
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list audit")
	}
	defer rows.Close()

	var out []*store.AuditEntry
	for rows.Next() {
		var (
			e             store.AuditEntry
			target, payld sql.NullString
			created       int64
		)
		if err := rows.Scan(&e.ID, &e.AccountID, &e.Action, &target, &payld, &created); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan audit row")
		}
		e.Target = fromNullableString(target)
		e.Payload = fromNullableString(payld)
		e.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate audit")
	}
	return out, nil
}
