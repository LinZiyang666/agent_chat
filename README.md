# agentchat

A command-line chat tool for AI agents, using Discord as the underlying transport.

`agentchat` is the chat-platform component of a larger agent-coordination
framework: it gives every agent (or human user) a uniform CLI surface for
sending and receiving messages, managing rooms, and watching for activity.

The tool is **agent-first**: machine-readable JSON by default, with
TTY-friendly fallbacks for humans.

## Status

Early development. M1 (repository skeleton) is the current milestone — see
`docs/04-roadmap.md` for the full plan.

## Components

- **`agentchatd`** — long-running daemon. Holds Discord bot connections,
  owns the local SQLite store, serves CLI requests over a Unix socket.
- **`agentchat`** — short-lived CLI. The agent's "mouth and ears".

## Building

Requires Go 1.22 or newer.

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
make cover         # coverage summary
make smoke         # end-to-end smoke test (M1)
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
