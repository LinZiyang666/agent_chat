package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

type roomRepo struct {
	db queryExecer
}

func (r *roomRepo) Create(ctx context.Context, room *store.Room) error {
	if room == nil {
		return errcode.New(errcode.InvalidArgument, "room is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO rooms(id, discord_channel_id, name, archived, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		room.ID, room.DiscordChannelID, room.Name,
		boolToInt(room.Archived), room.CreatedAt.Unix(), room.UpdatedAt.Unix(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.New(errcode.Conflict,
				"room with discord_channel_id %q already exists", room.DiscordChannelID)
		}
		return errcode.Wrap(err, errcode.Internal, "insert room")
	}
	return nil
}

func (r *roomRepo) Get(ctx context.Context, id string) (*store.Room, error) {
	return r.fetchOne(ctx,
		`SELECT id, discord_channel_id, name, archived, created_at, updated_at
		 FROM rooms WHERE id = ?`, id)
}

func (r *roomRepo) GetByDiscordChannelID(ctx context.Context, channelID string) (*store.Room, error) {
	return r.fetchOne(ctx,
		`SELECT id, discord_channel_id, name, archived, created_at, updated_at
		 FROM rooms WHERE discord_channel_id = ?`, channelID)
}

func (r *roomRepo) fetchOne(ctx context.Context, query, arg string) (*store.Room, error) {
	row := r.db.QueryRowContext(ctx, query, arg)
	var (
		room           store.Room
		archived       int
		createdAt, upd int64
	)
	err := row.Scan(&room.ID, &room.DiscordChannelID, &room.Name, &archived, &createdAt, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "room not found")
	}
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "scan room")
	}
	room.Archived = archived != 0
	room.CreatedAt = time.Unix(createdAt, 0).UTC()
	room.UpdatedAt = time.Unix(upd, 0).UTC()
	return &room, nil
}

func (r *roomRepo) List(ctx context.Context, includeArchived bool) ([]*store.Room, error) {
	q := `SELECT id, discord_channel_id, name, archived, created_at, updated_at
	      FROM rooms`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY created_at ASC, id ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list rooms")
	}
	defer rows.Close()
	return scanRooms(rows)
}

func (r *roomRepo) ListByMember(ctx context.Context, accountID string, includeArchived bool) ([]*store.Room, error) {
	q := `SELECT r.id, r.discord_channel_id, r.name, r.archived, r.created_at, r.updated_at
	      FROM rooms r
	      JOIN memberships m ON m.room_id = r.id
	      WHERE m.account_id = ?`
	if !includeArchived {
		q += ` AND r.archived = 0`
	}
	q += ` ORDER BY r.created_at ASC, r.id ASC`
	rows, err := r.db.QueryContext(ctx, q, accountID)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "list rooms by member")
	}
	defer rows.Close()
	return scanRooms(rows)
}

func scanRooms(rows *sql.Rows) ([]*store.Room, error) {
	var out []*store.Room
	for rows.Next() {
		var (
			room           store.Room
			archived       int
			createdAt, upd int64
		)
		if err := rows.Scan(&room.ID, &room.DiscordChannelID, &room.Name, &archived, &createdAt, &upd); err != nil {
			return nil, errcode.Wrap(err, errcode.Internal, "scan room row")
		}
		room.Archived = archived != 0
		room.CreatedAt = time.Unix(createdAt, 0).UTC()
		room.UpdatedAt = time.Unix(upd, 0).UTC()
		out = append(out, &room)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "iterate rooms")
	}
	return out, nil
}

func (r *roomRepo) Update(ctx context.Context, room *store.Room) error {
	if room == nil {
		return errcode.New(errcode.InvalidArgument, "room is nil")
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE rooms SET name = ?, archived = ?, updated_at = ? WHERE id = ?`,
		room.Name, boolToInt(room.Archived), room.UpdatedAt.Unix(), room.ID,
	)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "update room")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "room %s not found", room.ID)
	}
	return nil
}

func (r *roomRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM rooms WHERE id = ?`, id)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "delete room")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "rows affected")
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "room %s not found", id)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
