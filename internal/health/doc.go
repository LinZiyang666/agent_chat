// Package health tracks the daemon's view of external dependencies:
// Discord API reachability, bot-token validity, and network status.
// Surfaced as dimension 8 of the state view. See
// docs/02-requirements-final.md §5.6.
package health
