package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type tokenRepo struct {
	db queryExecer
}

func (r *tokenRepo) Create(ctx context.Context, t *store.Token) error {
	if t == nil {
		return errcode.New(errcode.InvalidArgument, "token is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO tokens(id, account_id, token_hash, created_at, revoked_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.AccountID, t.Hash, t.CreatedAt.Unix(),
		nullableUnix(t.RevokedAt), nullableUnix(t.LastUsedAt),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "insert token")
	}
	return nil
}

func (r *tokenRepo) Get(ctx context.Context, id string) (*store.Token, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, account_id, token_hash, created_at, revoked_at, last_used_at
FROM tokens WHERE id = ?`, id)
	return scanToken(row.Scan)
}

func (r *tokenRepo) ListByAccount(ctx context.Context, accountID string) ([]*store.Token, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, account_id, token_hash, created_at, revoked_at, last_used_at
FROM tokens
WHERE account_id = ?
ORDER BY created_at ASC`, accountID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list tokens")
	}
	defer rows.Close()

	var out []*store.Token
	for rows.Next() {
		tk, err := scanToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, tk)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate tokens")
	}
	return out, nil
}

func (r *tokenRepo) Revoke(ctx context.Context, id string, at time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		at.Unix(), id)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "revoke token")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		// Disambiguate "absent" from "already revoked".
		var dummy string
		err := r.db.QueryRowContext(ctx, `SELECT id FROM tokens WHERE id = ?`, id).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return errcode.New(errcode.NotFound, "token %s not found", id)
		}
		if err != nil {
			return errcode.Wrap(err, errcode.Internal, "lookup token")
		}
		return errcode.New(errcode.Conflict, "token %s already revoked", id)
	}
	return nil
}

func (r *tokenRepo) TouchLastUsed(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tokens SET last_used_at = ? WHERE id = ?`, at.Unix(), id)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "touch token")
	}
	return nil
}

// scanFunc abstracts *sql.Row.Scan and *sql.Rows.Scan so the column
// list lives in one place.
type scanFunc = func(dest ...any) error

func scanToken(scan scanFunc) (*store.Token, error) {
	var (
		t                   store.Token
		createdAt           int64
		revokedAt, lastUsed sql.NullInt64
	)
	err := scan(&t.ID, &t.AccountID, &t.Hash, &createdAt, &revokedAt, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "token not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan token")
	}
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.RevokedAt = fromNullableUnix(revokedAt)
	t.LastUsedAt = fromNullableUnix(lastUsed)
	return &t, nil
}
