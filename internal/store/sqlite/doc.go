// Package sqlite is the SQLite-backed implementation of the
// repository interfaces declared in internal/store. It uses
// modernc.org/sqlite (pure-Go, no cgo) and embeds its schema migrations
// as SQL files via go:embed.
//
// All time values are persisted as Unix seconds (INTEGER) so that the
// schema is portable across timezones and easy to inspect from a
// vanilla sqlite3 CLI.
package sqlite
