# M2 — Phase 1 Report (Implementation)

> Companion to `M2-phase2.md` (testing). Phase 1+2 executed as one
> continuous block per `05-engineering-workflow.md`.

**Date:** 2026-05-13
**Milestone scope:** `04-roadmap.md` §3 — daemon + accounts + auth (no Discord).

## 1. Goal recap

Stand up a running `agentchatd` daemon backed by SQLite, exposing
the /v1 HTTP surface (accounts CRUD, tokens, whoami, audit) on a Unix
domain socket, with bearer-token authentication and admin-only RBAC.
The `agentchat` CLI binds to this surface end-to-end. Discord is
**not** involved in M2.

## 2. Files added

### New top-level files

| File | Purpose |
|------|---------|
| `e2e/m2-smoke.sh` | End-to-end shell test driving both binaries through a real daemon + Unix socket |

### Daemon (`cmd/agentchatd/`)

| File | Purpose |
|------|---------|
| `cmds/serve.go` | `agentchatd serve` command: config load, master-key bootstrap, SQLite open + migrate, root admin bootstrap (prints one-time token), Unix-socket listener, graceful shutdown on SIGINT/SIGTERM |
| `cmds/serve_test.go` | Unit tests for `removeStaleSocket` and `newLogger` |
| `cmds/root.go` | **Modified** — `SilenceErrors: true` (M1-P3-001 fix) |
| `main.go` | **Modified** — single `cliutil.PrintAndExit` call, no duplicate blank stderr line |

### CLI (`cmd/agentchat/`)

| File | Purpose |
|------|---------|
| `cmds/root.go` | **Modified** — persistent flags (`--token`, `--socket`, `--json`, `--no-color`), token/socket resolvers, `newClient()` helper, `SilenceErrors: true` |
| `cmds/output.go` | TTY detection, JSON-vs-table output mode, table renderer using `text/tabwriter`, time formatters |
| `cmds/whoami.go` | `agentchat whoami` |
| `cmds/admin.go` | Parent of all `admin *` commands |
| `cmds/admin_account.go` | `admin account create / list / show / delete / set-role / rename` |
| `cmds/admin_token.go` | `admin token create / list / revoke` |
| `cmds/admin_audit.go` | `admin audit list [--account] [--since] [--limit]` |
| `main.go` | **Modified** — single `cliutil.PrintAndExit` call |

### Internal packages

| Package | Purpose |
|---------|---------|
| `internal/errcode/` | `Code` enum, `Error` type with `Code/Message/Details/cause`, `errors.Is/As` integration, `New/Wrap/As/WithDetails`, `ExitCode`, `HTTPStatus` |
| `internal/config/` | `Config` struct (DataRoot, SocketPath, DBPath, KeyPath, Log), `Defaults`, `Load` (TOML + env), `EnsureDataRoot`, `finalize` |
| `internal/crypto/` | `HashAPIToken` (bcrypt cost 12), `VerifyAPIToken`, `RandomSecret` (32 random bytes → base64url), `LoadOrCreateMasterKey` (32-byte AES-GCM key, 0o600) |
| `internal/store/` | Domain types (`Account`, `Token`, `AuditEntry`, `Role`, `LifecycleState`, `AuditFilter`); repository interfaces (`AccountRepo`, `TokenRepo`, `AuditRepo`); `Bundle` aggregator |
| `internal/store/sqlite/` | SQLite-backed repos (modernc.org/sqlite, pure Go); embedded migrations; `Store` with `Open` (WAL + foreign keys + NORMAL sync); time stored as Unix seconds; cascade delete on accounts |
| `internal/store/sqlite/migrations/0001_init.up.sql` | accounts + tokens + audit_log + indexes |
| `internal/auth/` | `Manager` (Issue/Verify/Revoke), token format `agch_<uuidv7-hex>_<base64url-secret>`, bcrypt verification, `Middleware` (Bearer header → context account), `RequireAdmin` |
| `internal/audit/` | `Recorder` wrapper around `AuditRepo` with `Action` enum, JSON payload marshaling, UUIDv7 IDs |
| `internal/account/` | `Service` (Create, Get, GetByName, List, SetRole, Rename, Delete, BootstrapRoot); name validation; UUIDv7 IDs |
| `internal/api/` | `Deps` struct; `NewRouter` (chi v5) with `/v1/healthz` (public), `/v1/whoami` (auth), `/v1/accounts*` + `/v1/tokens/{id}` + `/v1/audit` (auth+admin) |
| `internal/api/middleware/` | `Recover` (panic → INTERNAL response, log), `Logger` (one slog record per request) |
| `internal/api/v1/` | Request/response DTOs (`types.go`), JSON+error helpers (`helpers.go`), handlers for healthz, whoami, accounts, tokens, audit |
| `internal/cliutil/` | `PrintError` + `PrintAndExit` (single error format point for both binaries), `IsTerminal` (cross-platform via mattn/go-isatty) |

### Public client (`pkg/client/`)

| File | Purpose |
|------|---------|
| `client.go` | Unix-socket HTTP client; typed methods for every M2 endpoint; network errors mapped to `errcode.Unavailable`; structured error decoding |
| `client_test.go` | Integration tests against a real daemon stack on a Unix socket |

## 3. Dependencies added

Per workflow §1.8 audit trail (URL + version + check date):

| Package | Version | Docs URL | Checked |
|---------|---------|----------|---------|
| `github.com/go-chi/chi/v5` | v5.2.5 | https://github.com/go-chi/chi | 2026-05-13 |
| `modernc.org/sqlite` | v1.50.1 | https://pkg.go.dev/modernc.org/sqlite | 2026-05-13 |
| `github.com/BurntSushi/toml` | v1.6.0 | https://github.com/BurntSushi/toml | 2026-05-13 |
| `github.com/google/uuid` | v1.6.0 | https://github.com/google/uuid | 2026-05-13 |
| `golang.org/x/crypto/bcrypt` | v0.51.0 (parent: golang.org/x/crypto) | https://pkg.go.dev/golang.org/x/crypto/bcrypt | 2026-05-13 |
| `github.com/stretchr/testify` | v1.11.1 | https://github.com/stretchr/testify | 2026-05-13 |
| `github.com/mattn/go-isatty` | v0.0.20 | (transitive; consulted for TTY detection) | 2026-05-13 |

## 4. Deviations from the architecture / decisions made in flight

1. **Go directive raised from 1.22 → 1.25** (forced by x/crypto v0.51.0
   which requires Go 1.25). Architecture doc said "1.22+"; 1.25 still
   satisfies "+", but worth noting. Toolchain auto-downloaded Go 1.25.10
   via the `toolchain` directive in go.mod. **No action needed**; the
   project still builds on any machine with Go ≥ 1.25.
2. **Token wire format** = `agch_<hex_uuid_v7_32chars>_<base64url_32_bytes>`.
   The token ID is the dash-stripped hex of a UUIDv7, exposed in clear
   text so lookup is O(1). The secret half (43 base64url chars) is the
   only secret; only its bcrypt hash is stored. Pattern modeled on
   GitHub Personal Access Tokens.
3. **bcrypt cost 12** (not the package default of 10). Justification
   inline in `internal/crypto/bcrypt.go`: long-lived tokens warrant
   stronger work; CLI auth still finishes under perceptible threshold.
4. **SQLite `max_open_conns = 1`** to sidestep SQLITE_BUSY under
   concurrent writers. The daemon is single-process; in-process reads
   are cheap. If M5's state aggregator ever creates a hot path we'll
   revisit with a separate read connection pool.
5. **All times stored as Unix seconds (INTEGER)**, not native
   SQLite DATETIME. Portable across timezones and inspectable from
   `sqlite3` CLI without surprises. Time conversions all go through
   helpers in `db.go` (`nullableUnix`, `fromNullableUnix`).
6. **Module-internal `pkg/client`**: `pkg/client` imports
   `internal/api/v1` for the DTOs and `internal/errcode` for the
   `*Error` type. This means `pkg/client` cannot today be extracted to
   a separate Go module without moving those types. Acceptable trade-off
   for M2; revisit at M8 if we package an SDK.
7. **Foreign-key cascade on `accounts.id → tokens.account_id`**.
   Deleting an account purges its tokens. Exercised by a test.
8. **Healthz is public** (no auth). Liveness probes don't need it.
9. **Logger middleware logs everything** including 200s. M2 has a
   handful of endpoints; in M8 we may add per-route log levels.
10. **Audit log default limit = 500** rows when `Limit=0`. Prevents
    accidental full-table dumps; explicit `?limit=N` overrides.
11. **`SilenceErrors: true` on both root commands + central
    `cliutil.PrintAndExit`** — closes M1-P3-001 (no more duplicated
    blank stderr line on errors).
12. **JSON-on-pipe by default** — `outputJSON()` returns true when
    stdout is not a TTY *or* `--json` is passed. Agents always see JSON;
    humans on a terminal see tables.

## 5. M1 audit follow-ups completed

| M1 audit issue | Status in M2 |
|---|---|
| **M1-P3-001** Error path prints extra blank stderr line | **Fixed.** Both root commands now use `SilenceErrors: true`; `main()` delegates to `cliutil.PrintAndExit` which formats once and exits with the mapped code. The legacy `fmt.Fprintln(os.Stderr)` blank line is gone. |
| **M1-P3-002** Dependency docs lack URL/date | **Fixed.** Workflow §1.8 was amended at M1 closeout to require URL + version + date. M2 phase1 §3 above complies. |

## 6. Deferred items (intentional, not gaps)

- **Cobra shell completion** — comes in M8.
- **Bot-token AES-GCM encrypt/decrypt** — the column exists and the
  master key is bootstrapped, but no real ciphertext is written until
  M3 ships Discord adapter.
- **`~/.agentchat/cli.toml` config file** — `agentchat`'s help text
  mentions it as the third-priority token source; the loader will land
  in M8.
- **Cross-platform Windows test** — relies on Unix sockets, which
  Windows supports inconsistently. Out of scope.
- **Per-route rate limit / quota** — single-user local tool, not
  necessary.

## 7. Demo (reproduction of milestone goal)

```bash
# Build.
make build

# Start the daemon in a fresh data root. (Foreground in shell 1.)
./bin/agentchatd serve --data-root /tmp/m2demo
# The daemon prints:
#   AGENTCHAT_TOKEN=agch_<...>
# Save that.

# Shell 2.
export AGENTCHAT_TOKEN=<paste>
export AGENTCHAT_HOME=/tmp/m2demo

./bin/agentchat whoami
./bin/agentchat admin account create --name alice --role user
./bin/agentchat admin account list
./bin/agentchat admin token create <alice-id>
./bin/agentchat admin audit list
```

The end-to-end script `e2e/m2-smoke.sh` automates the entire flow,
including spinning the daemon up, parsing its bootstrap banner, and
asserting on outputs.

## 8. Layout snapshot (M2 additions)

```
internal/account/        service.go        + service_test.go
internal/api/            server.go         + api_test.go
internal/api/middleware/ recover.go logger.go
internal/api/v1/         types.go helpers.go whoami.go accounts.go
                         tokens.go audit.go healthz.go
internal/audit/          audit.go          + audit_test.go
internal/auth/           token.go middleware.go + auth_test.go
internal/cliutil/        cliutil.go        + cliutil_test.go
internal/config/         config.go         + config_test.go
internal/crypto/         bcrypt.go master_key.go + bcrypt_test.go + master_key_test.go
internal/errcode/        errcode.go exitcode.go http.go + errcode_test.go
internal/store/          store.go types.go
internal/store/sqlite/   doc.go db.go account_repo.go token_repo.go
                         audit_repo.go migrations/0001_init.up.sql
                         + sqlite_test.go
pkg/client/              client.go         + client_test.go
cmd/agentchat/cmds/      output.go whoami.go admin.go admin_account.go
                         admin_token.go admin_audit.go (root/main modified)
cmd/agentchatd/cmds/     serve.go          + serve_test.go (root/main modified)
e2e/                     m2-smoke.sh
docs/milestones/         M2-phase1.md M2-phase2.md
```
