// Package state is the aggregation engine for the per-account state
// view that agents subscribe to. It maintains the 8 dimensions of
// summary data (unread counts, mentions, pending acks, system health,
// etc.) and pushes debounced snapshots to subscribers. See
// docs/02-requirements-final.md §5.2 and docs/03-architecture.md D5.
package state
