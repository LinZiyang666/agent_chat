package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite-backed implementation. It owns the *sql.DB
// handle and exposes the three repositories via Bundle().
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at the given filesystem
// path, applies any pending migrations, and returns a ready-to-use
// Store. Pass ":memory:" for an in-memory test DB.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "open sqlite %s", path)
	}
	// SQLite is single-writer by design. Capping max-open to 1 avoids
	// SQLITE_BUSY surprises under concurrent access from our handlers;
	// in-process reads are cheap enough.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errcode.Wrap(err, errcode.Internal, "ping sqlite %s", path)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying *sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for callers that need raw access
// (currently: tests). Production code must go through Bundle().
func (s *Store) DB() *sql.DB { return s.db }

// Bundle returns the repositories wired to this Store.
func (s *Store) Bundle() store.Bundle {
	return store.Bundle{
		Accounts:                &accountRepo{db: s.db},
		Tokens:                  &tokenRepo{db: s.db},
		Audit:                   &auditRepo{db: s.db},
		Rooms:                   &roomRepo{db: s.db},
		Memberships:             &membershipRepo{db: s.db},
		Messages:                &messageRepo{db: s.db},
		MessageStates:           &messageStateRepo{db: s.db},
		Announcements:           &announcementRepo{db: s.db},
		AnnouncementReads:       &announcementReadRepo{db: s.db},
		SystemAnnouncements:     &systemAnnouncementRepo{db: s.db},
		SystemAnnouncementReads: &systemAnnouncementReadRepo{db: s.db},
		Attachments:             &attachmentRepo{db: s.db},
	}
}

// queryExecer is the subset of *sql.DB and *sql.Tx used by every
// repository. Switching repos to depend on this interface lets the
// same code execute either against the global handle or against a
// transaction (whichever is passed at construction).
type queryExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// WithTx satisfies store.Bundler. It executes fn inside a single SQLite
// transaction: every Bundle method called against b writes through
// that transaction, so a mutation + its audit row commit (or roll
// back) as one unit.
//
// Concurrent callers serialize on the underlying single connection
// (max_open_conns = 1, see Open). For our scale this is acceptable;
// if a future milestone needs concurrent writers we can grow the pool
// and add SQLite's BEGIN IMMEDIATE semantics.
func (s *Store) WithTx(ctx context.Context, fn func(b store.Bundle) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "begin transaction")
	}
	// Always call Rollback; it is a no-op after a successful Commit.
	defer func() { _ = tx.Rollback() }()

	txBundle := store.Bundle{
		Accounts:                &accountRepo{db: tx},
		Tokens:                  &tokenRepo{db: tx},
		Audit:                   &auditRepo{db: tx},
		Rooms:                   &roomRepo{db: tx},
		Memberships:             &membershipRepo{db: tx},
		Messages:                &messageRepo{db: tx},
		MessageStates:           &messageStateRepo{db: tx},
		Announcements:           &announcementRepo{db: tx},
		AnnouncementReads:       &announcementReadRepo{db: tx},
		SystemAnnouncements:     &systemAnnouncementRepo{db: tx},
		SystemAnnouncementReads: &systemAnnouncementReadRepo{db: tx},
		Attachments:             &attachmentRepo{db: tx},
	}
	if err := fn(txBundle); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errcode.Wrap(err, errcode.Internal, "commit transaction")
	}
	return nil
}

// buildDSN returns a modernc/sqlite DSN with the pragmas we always want.
// We use WAL for crash safety and concurrent readers, NORMAL synchronous
// (a sound default for laptops/dev boxes; durability still good thanks
// to WAL), and enable foreign keys (off by default in SQLite).
//
// For ":memory:" we pass it through unchanged; WAL has no benefit there.
func buildDSN(path string) string {
	if path == ":memory:" {
		return "file::memory:?cache=shared&_pragma=foreign_keys(1)"
	}
	// M8-Q-P1-014: removed `_time_format=sqlite` — that pragma is a
	// `mattn/go-sqlite3` DSN option, not a `modernc.org/sqlite` one,
	// so it was silently ignored. We store timestamps as Unix seconds
	// via nullableUnix anyway, so the absent option is correct.
	return "file:" + path + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}, "&")
}

// migrate applies any embedded SQL migrations not yet recorded in
// schema_migrations.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);`); err != nil {
		return errcode.Wrap(err, errcode.Internal, "create schema_migrations")
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "read embedded migrations")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		v, err := parseMigrationVersion(name)
		if err != nil {
			return err
		}
		var dummy int
		row := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM schema_migrations WHERE version = ?`, v)
		if err := row.Scan(&dummy); err == nil {
			continue
		} else if err != sql.ErrNoRows {
			return errcode.Wrap(err, errcode.Internal, "check migration %d", v)
		}

		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return errcode.Wrap(err, errcode.Internal, "read migration %s", name)
		}
		if _, err := s.db.ExecContext(ctx, string(body)); err != nil {
			return errcode.Wrap(err, errcode.Internal, "apply migration %s", name)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			v, time.Now().Unix(),
		); err != nil {
			return errcode.Wrap(err, errcode.Internal, "record migration %d", v)
		}
	}
	return nil
}

// parseMigrationVersion extracts the leading numeric prefix from a
// migration filename like "0001_init.up.sql".
func parseMigrationVersion(name string) (int, error) {
	var v int
	if _, err := fmt.Sscanf(name, "%04d_", &v); err != nil {
		return 0, errcode.Wrap(err, errcode.Internal, "parse migration version from %s", name)
	}
	return v, nil
}

// isUniqueViolation tests whether err is a SQLite UNIQUE-constraint
// failure. We match on the message text because modernc.org/sqlite's
// typed error needs an indirect import we'd rather not pin.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// nullableUnix converts a *time.Time into a sql.NullInt64 for storage.
func nullableUnix(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

// fromNullableUnix is the inverse of nullableUnix.
func fromNullableUnix(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	tt := time.Unix(n.Int64, 0).UTC()
	return &tt
}

// nullableString converts a string into a sql.NullString. Empty string
// is treated as NULL on the way in.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func fromNullableString(n sql.NullString) string {
	if !n.Valid {
		return ""
	}
	return n.String
}
