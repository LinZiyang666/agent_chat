package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type membershipRepo struct {
	db queryExecer
}

func (r *membershipRepo) Upsert(ctx context.Context, m *store.Membership) error {
	if m == nil {
		return errcode.New(errcode.InvalidArgument, "membership is nil")
	}
	// ON CONFLICT keeps the existing joined_at (no resetting on a
	// subscribe-toggle) and updates the subscribed flag only.
	_, err := r.db.ExecContext(ctx, `
INSERT INTO memberships(account_id, room_id, subscribed, joined_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(account_id, room_id) DO UPDATE
   SET subscribed = excluded.subscribed`,
		m.AccountID, m.RoomID, boolToInt(m.Subscribed), m.JoinedAt.Unix(),
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "upsert membership")
	}
	return nil
}

func (r *membershipRepo) Get(ctx context.Context, accountID, roomID string) (*store.Membership, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT account_id, room_id, subscribed, joined_at
  FROM memberships WHERE account_id = ? AND room_id = ?`,
		accountID, roomID)
	var (
		m          store.Membership
		subscribed int
		joinedAt   int64
	)
	err := row.Scan(&m.AccountID, &m.RoomID, &subscribed, &joinedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "membership not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan membership")
	}
	m.Subscribed = subscribed != 0
	m.JoinedAt = time.Unix(joinedAt, 0).UTC()
	return &m, nil
}

func (r *membershipRepo) ListByRoom(ctx context.Context, roomID string) ([]*store.Membership, error) {
	return r.listWhere(ctx,
		`SELECT account_id, room_id, subscribed, joined_at
		 FROM memberships WHERE room_id = ? ORDER BY joined_at ASC`,
		roomID)
}

func (r *membershipRepo) ListByAccount(ctx context.Context, accountID string) ([]*store.Membership, error) {
	return r.listWhere(ctx,
		`SELECT account_id, room_id, subscribed, joined_at
		 FROM memberships WHERE account_id = ? ORDER BY joined_at ASC`,
		accountID)
}

// ListByAccountWithRooms returns one row per membership joined with
// the corresponding room — replaces the M5 aggregator's N+1
// Rooms.Get loop (M5-P3-006 fix).
func (r *membershipRepo) ListByAccountWithRooms(ctx context.Context, accountID string) ([]*store.MembershipWithRoom, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT mb.account_id, mb.room_id, mb.subscribed, mb.joined_at,
       rm.id, rm.discord_channel_id, rm.name, rm.archived, rm.created_at, rm.updated_at
  FROM memberships mb
  JOIN rooms       rm ON rm.id = mb.room_id
 WHERE mb.account_id = ?
 ORDER BY mb.joined_at ASC`, accountID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list memberships with rooms")
	}
	defer rows.Close()
	var out []*store.MembershipWithRoom
	for rows.Next() {
		var (
			m          store.Membership
			subscribed int
			joinedAt   int64
			room       store.Room
			archived   int
			rCreatedAt int64
			rUpdatedAt int64
		)
		if err := rows.Scan(
			&m.AccountID, &m.RoomID, &subscribed, &joinedAt,
			&room.ID, &room.DiscordChannelID, &room.Name, &archived, &rCreatedAt, &rUpdatedAt,
		); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan membership+room row")
		}
		m.Subscribed = subscribed != 0
		m.JoinedAt = time.Unix(joinedAt, 0).UTC()
		room.Archived = archived != 0
		room.CreatedAt = time.Unix(rCreatedAt, 0).UTC()
		room.UpdatedAt = time.Unix(rUpdatedAt, 0).UTC()
		out = append(out, &store.MembershipWithRoom{
			Membership: &m,
			Room:       &room,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate memberships+rooms")
	}
	return out, nil
}

func (r *membershipRepo) ListSubscribers(ctx context.Context, roomID string) ([]*store.Membership, error) {
	return r.listWhere(ctx,
		`SELECT account_id, room_id, subscribed, joined_at
		 FROM memberships WHERE room_id = ? AND subscribed = 1 ORDER BY joined_at ASC`,
		roomID)
}

func (r *membershipRepo) listWhere(ctx context.Context, q, arg string) ([]*store.Membership, error) {
	rows, err := r.db.QueryContext(ctx, q, arg)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list memberships")
	}
	defer rows.Close()
	var out []*store.Membership
	for rows.Next() {
		var (
			m          store.Membership
			subscribed int
			joinedAt   int64
		)
		if err := rows.Scan(&m.AccountID, &m.RoomID, &subscribed, &joinedAt); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan membership row")
		}
		m.Subscribed = subscribed != 0
		m.JoinedAt = time.Unix(joinedAt, 0).UTC()
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate memberships")
	}
	return out, nil
}

func (r *membershipRepo) SetSubscribed(ctx context.Context, accountID, roomID string, subscribed bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE memberships SET subscribed = ? WHERE account_id = ? AND room_id = ?`,
		boolToInt(subscribed), accountID, roomID)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "set membership subscribed")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "membership not found")
	}
	return nil
}

func (r *membershipRepo) Delete(ctx context.Context, accountID, roomID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM memberships WHERE account_id = ? AND room_id = ?`,
		accountID, roomID)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "delete membership")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "membership not found")
	}
	return nil
}
