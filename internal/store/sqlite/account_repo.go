package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type accountRepo struct {
	db queryExecer
}

func (r *accountRepo) Create(ctx context.Context, a *store.Account) error {
	if a == nil {
		return errcode.New(errcode.InvalidArgument, "account is nil")
	}
	if !a.Role.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid role: %q", a.Role)
	}
	if !a.LifecycleState.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid lifecycle state: %q", a.LifecycleState)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO accounts(id, name, role, lifecycle_state, bot_token_enc, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Name, string(a.Role), string(a.LifecycleState),
		a.BotTokenEnc, a.CreatedAt.Unix(), a.UpdatedAt.Unix(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.New(errcode.Conflict, "account name %q already exists", a.Name).
				WithDetails(map[string]any{"name": a.Name})
		}
		return errcode.Wrap(err, errcode.Internal, "insert account")
	}
	return nil
}

func (r *accountRepo) Get(ctx context.Context, id string) (*store.Account, error) {
	return r.fetchOne(ctx,
		`SELECT id, name, role, lifecycle_state, bot_token_enc, created_at, updated_at
		 FROM accounts WHERE id = ?`, id)
}

func (r *accountRepo) GetByName(ctx context.Context, name string) (*store.Account, error) {
	return r.fetchOne(ctx,
		`SELECT id, name, role, lifecycle_state, bot_token_enc, created_at, updated_at
		 FROM accounts WHERE name = ?`, name)
}

func (r *accountRepo) fetchOne(ctx context.Context, query string, arg string) (*store.Account, error) {
	row := r.db.QueryRowContext(ctx, query, arg)
	var (
		a              store.Account
		role, state    string
		createdAt, upd int64
	)
	err := row.Scan(&a.ID, &a.Name, &role, &state, &a.BotTokenEnc, &createdAt, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "account not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan account")
	}
	a.Role = store.Role(role)
	a.LifecycleState = store.LifecycleState(state)
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	a.UpdatedAt = time.Unix(upd, 0).UTC()
	return &a, nil
}

func (r *accountRepo) List(ctx context.Context) ([]*store.Account, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, role, lifecycle_state, bot_token_enc, created_at, updated_at
		 FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list accounts")
	}
	defer rows.Close()

	var out []*store.Account
	for rows.Next() {
		var (
			a              store.Account
			role, state    string
			createdAt, upd int64
		)
		if err := rows.Scan(&a.ID, &a.Name, &role, &state, &a.BotTokenEnc, &createdAt, &upd); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan account row")
		}
		a.Role = store.Role(role)
		a.LifecycleState = store.LifecycleState(state)
		a.CreatedAt = time.Unix(createdAt, 0).UTC()
		a.UpdatedAt = time.Unix(upd, 0).UTC()
		out = append(out, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate accounts")
	}
	return out, nil
}

func (r *accountRepo) Update(ctx context.Context, a *store.Account) error {
	if a == nil {
		return errcode.New(errcode.InvalidArgument, "account is nil")
	}
	if !a.Role.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid role: %q", a.Role)
	}
	if !a.LifecycleState.Valid() {
		return errcode.New(errcode.InvalidArgument, "invalid lifecycle state: %q", a.LifecycleState)
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE accounts
   SET name = ?, role = ?, lifecycle_state = ?, bot_token_enc = ?, updated_at = ?
 WHERE id = ?`,
		a.Name, string(a.Role), string(a.LifecycleState), a.BotTokenEnc, a.UpdatedAt.Unix(), a.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.New(errcode.Conflict, "account name %q already exists", a.Name)
		}
		return errcode.Wrap(err, errcode.Internal, "update account")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "account %s not found", a.ID)
	}
	return nil
}

func (r *accountRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "delete account")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "account %s not found", id)
	}
	return nil
}

func (r *accountRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&n); err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "count accounts")
	}
	return n, nil
}
