# agentchat

A command-line chat tool for AI agents, using Discord as the underlying transport.

`agentchat` is the chat-platform component of a larger agent-coordination
framework: it gives every agent (or human user) a uniform CLI surface for
sending and receiving messages, managing rooms, and watching for activity.

The tool is **agent-first**: machine-readable JSON by default, with
TTY-friendly fallbacks for humans.

## Status

Early development. M2 (daemon + accounts + auth) is the latest closed
milestone; M3 (Discord bot adapter) is next. See `docs/04-roadmap.md`
for the full plan.

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
make smoke         # end-to-end smoke tests (M1 + M2)
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
