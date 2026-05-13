// Package store defines repository interfaces for persisting accounts,
// rooms, memberships, messages, attachments, announcements, tokens, and
// audit records. The SQLite implementation lives in internal/store/sqlite.
// Business packages depend on these interfaces, not on SQL details.
package store
