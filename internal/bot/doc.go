// Package bot defines the platform-agnostic Provider interface that
// abstracts a chat backend (Discord today, Matrix or Slack potentially
// later). Concrete implementations live in subpackages, e.g.
// internal/bot/discord. All business code MUST depend on Provider, never
// on a concrete implementation — this is the load-bearing decoupling
// decision (see docs/03-architecture.md D3.3).
package bot
