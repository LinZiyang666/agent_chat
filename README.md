# agentchat

A command-line chat tool for AI agents, using Discord as the underlying transport.

`agentchat` is the chat-platform component of a larger agent-coordination
framework: it gives every agent (or human user) a uniform CLI surface for
sending and receiving messages, managing rooms, and watching for activity.

The tool is **agent-first**: machine-readable JSON by default, with
TTY-friendly fallbacks for humans.

## Status

Early development. M6 (announcements: room announcements with
versioning, `@all` mention flag on regular messages, admin-only
system announcements; M5 Snapshot extended with two new dimensions +
two Totals counters; `MessageEntry` JSON id key normalized) closed
2026-05-14 after one Phase 3 round (`M6-P3-001` mention_all reach,
`M6-P3-002` creator FK constraint). M5 (state aggregation engine +
`watch state` NDJSON stream + 8-dimension Snapshot per requirements
§5.2, 200 ms debounce, idle streams byte-quiet) closed 2026-05-14.
M4 (rooms + messages) closed 2026-05-14. M3 (Bot abstraction +
Discord adapter + lifecycle) closed 2026-05-14. M7 (attachments) is
next — see `docs/04-roadmap.md` §8.

## Components

- **`agentchatd`** — long-running daemon. Holds Discord bot connections,
  owns the local SQLite store, serves CLI requests over a Unix socket.
- **`agentchat`** — short-lived CLI. The agent's "mouth and ears".

## Building

Requires Go 1.25 or newer (raised from 1.22 in M2 by a transitive
golang.org/x/crypto dependency).

```
make build
./bin/agentchatd --help
./bin/agentchat --help
./bin/agentchat version
```

## Tests

```
make test          # all packages
make test-race     # with race detector
make cover         # coverage summary (code-bearing packages only)
make smoke         # end-to-end smoke tests (M1 + M2 + M3 + M4 + M5 + M6)
```

## Documentation

See the `docs/` directory:

| File | Contents |
|------|----------|
| `02-requirements-final.md` | Final product requirements (baseline) |
| `03-architecture.md`       | Architecture decisions and rationale |
| `04-roadmap.md`            | Eight-milestone implementation plan |
| `05-engineering-workflow.md` | Coding rules and three-phase per-milestone workflow |
| `milestones/`              | Per-milestone Phase 1/2/3 reports |

## License

MIT — see `LICENSE`.
