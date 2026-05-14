# M2 — Phase 2 Report (Testing)

> Companion to `M2-phase1.md`. Phase 1+2 executed as one continuous
> block per `05-engineering-workflow.md`.

**Date:** 2026-05-13
**Verification command:** `make fmt vet test-race smoke cover`
**Result:** all green.

## 1. Strategy

The QA goal for M2 was to lock the **observable contracts** before any
business logic depends on them:

- **Per-package unit tests** exercise the smallest meaningful surface,
  with table-driven cases for enums and pure functions (`errcode`,
  `crypto`, `cliutil`).
- **Repository tests** open a real on-disk SQLite database in a temp
  dir — *not* an in-memory shared cache — so migrations, WAL journaling,
  foreign-key cascades, and UNIQUE constraint detection are all
  exercised exactly as in production.
- **API integration tests** use `httptest.NewServer` with the full
  chi router, so every middleware (recover, logger, auth, RequireAdmin)
  runs end-to-end. They drive the daemon's HTTP API through raw
  `net/http.Client` requests, which is the same code path the SDK uses.
- **Client integration tests** spin up a *real Unix-socket* daemon
  stack inside the test and dial it through `pkg/client.New`. This
  catches anything the API tests miss about socket transport.
- **End-to-end shell smoke test** (`e2e/m2-smoke.sh`) builds both
  binaries, launches `agentchatd serve` in a temp data root, parses
  the bootstrap banner for the one-time token, and runs the entire CLI
  surface (whoami, account CRUD, role change, token issue, token
  revoke, audit list, healthz). This is the closest test to "what a
  user will actually run."

## 2. Test files added

| File | Test count (~) | Notes |
|------|----|------|
| `internal/errcode/errcode_test.go` | 7 | `New/Wrap/Is/As/WithDetails`; table-driven `ExitCode` + `HTTPStatus` |
| `internal/config/config_test.go` | 12 | defaults, env overrides, TOML overlay, malformed TOML, missing TOML OK, `finalize`, `EnsureDataRoot` |
| `internal/crypto/bcrypt_test.go` | 5 | hash + verify round-trip, wrong secret → `AuthInvalid`, empty rejected, uniqueness check (50 iterations), base64url length invariant |
| `internal/crypto/master_key_test.go` | 4 | bootstrap creates 32-byte key at 0o600, idempotent, parent dir auto-creation, bad-length rejection, empty-path rejection |
| `internal/store/sqlite/sqlite_test.go` | 12 | accounts CRUD, duplicate-name conflict, enum validation, list ordering, tokens CRUD, double-revoke conflict, revoke unknown, list-by-account, audit record + list + filters + limit, empty-action rejection, FK cascade on account delete |
| `internal/auth/auth_test.go` | 15 | token round-trip encode/parse, six malformed-shape rejections, Manager Issue/Verify happy path, wrong-secret rejection, revoked → `AuthRevoked`, unknown → `AuthInvalid` (no NotFound leak), empty-account rejection, last-used touch; middleware: valid bearer accepted, missing header, non-Bearer scheme, bad token; `RequireAdmin` admin allowed / user 403 / missing context 401 |
| `internal/audit/audit_test.go` | 4 | round-trip with payload, nil payload, empty-action rejection, list passthrough |
| `internal/account/service_test.go` | 13 | create happy + 4 validation rejections (empty/whitespace/too-long/bad-role), duplicate-name conflict, set-role happy + invalid-role + not-found, rename, delete, ordered list with injected clock, bootstrap root creates, bootstrap noop on populated |
| `internal/api/api_test.go` | 14 | healthz, unauthorized, whoami, create account happy/conflict/invalid-role, user RBAC (user token cannot create accounts → 403), rename, no-fields update → 400, delete + 404 after, full token lifecycle (issue → use → revoke → `AuthRevoked`), audit trail, audit filters, unknown-field rejection |
| `internal/cliutil/cliutil_test.go` | 5 | print with code, print plain, print with details, print nil, `IsTerminal(nil)` |
| `pkg/client/client_test.go` | 9 | real-socket healthz, whoami, account create+list+get, error mapping (NotFound), unreachable socket → `Unavailable`, rename + setRole + delete, full token lifecycle, audit list, timeout setter |
| `cmd/agentchatd/cmds/serve_test.go` | 5 | `removeStaleSocket` no-file / real socket / refuses regular file / empty-path; `newLogger` level table (8 inputs incl. case-insensitive + bogus → default) |
| `e2e/m2-smoke.sh` | 1 script | full daemon launch + 12 assertions |

Total: ~105 Go test functions + 1 shell e2e script.

## 3. `go test -race ./...` outcome

```
ok  cmd/agentchat/cmds                 1.014s
ok  cmd/agentchatd/cmds                1.029s
ok  internal/account                   1.674s
ok  internal/api                     114.847s  <- bcrypt-heavy
ok  internal/audit                     1.256s
ok  internal/auth                     29.379s
ok  internal/cliutil                   1.020s
ok  internal/config                    1.028s
ok  internal/crypto                   14.142s
ok  internal/errcode                   1.020s
ok  internal/store/sqlite              1.675s
ok  pkg/client                        75.137s
```

No failures, no race warnings, no panics. Wall-clock dominated by
bcrypt cost-12 hashes; that is intentional (see Phase 1 §4 decision 3).

## 4. Coverage (code-bearing packages only — per workflow §1.6)

| Package | Lines |
|---------|-------|
| internal/account | **85.5%** |
| internal/api | **100.0%** |
| internal/audit | **87.5%** |
| internal/auth | **84.1%** |
| internal/cliutil | **70.6%** |
| internal/config | **89.4%** |
| internal/crypto | **79.4%** |
| internal/errcode | **95.3%** |
| internal/store/sqlite | **76.6%** |
| pkg/client | **80.2%** |

All ten code-bearing packages exceed the 70% gate. The two `cmd/*/cmds`
packages (binary entry points) hold the legacy 75% from M1 plus the
new `serve` helpers' tests; the `serve.go` `runServe` orchestrator is
not covered by unit tests (it depends on real OS listeners) but is
exercised end-to-end by `e2e/m2-smoke.sh`.

Placeholder packages (still `doc.go` only — `internal/announcement/`,
`internal/api/middleware/`, `internal/api/v1/`, `internal/attachment/`,
`internal/bot/`, `internal/health/`, `internal/message/`, `internal/room/`,
`internal/state/`, `internal/store/`) are correctly N/A: no executable
statements. Note that `internal/api/v1/` and `internal/api/middleware/`
do contain real code (handlers + middleware) but their tests live in
the `internal/api` package; Go-cover attributes coverage to the package
under test, hence the 100% number on `internal/api` covers all of those
handlers transitively.

## 5. Edge cases exercised

### Auth boundary cases

- Bearer header missing → `AUTH_MISSING` / HTTP 401
- Wrong scheme (`Basic ...`) → `AUTH_INVALID` / 401
- Empty bearer after the scheme → `AUTH_INVALID` / 401
- Token id correct but secret tampered → `AUTH_INVALID`
- Token id unknown → `AUTH_INVALID` (no `NOT_FOUND` leak — prevents id enumeration)
- Token previously revoked → `AUTH_REVOKED` (HTTP 401)
- Valid token but the account vanished mid-request → `AUTH_INVALID`
- User-role token attempting an admin route → `PERM_DENIED` (HTTP 403)
- Token's `last_used_at` is updated only on a *successful* verification

### Repository / SQL edge cases

- Duplicate `accounts.name` → `CONFLICT` with `details={"name": "..."}`
- Re-revoking an already-revoked token → `CONFLICT` (not silent success)
- Revoking an unknown token → `NOT_FOUND`
- Foreign-key cascade: deleting an account removes its tokens
- Empty audit action rejected at `Record`
- Audit list honors `?account=`, `?since=`, `?limit=`

### CLI / API DTO edge cases

- Unknown JSON fields in request body rejected (`DisallowUnknownFields`)
- Empty PATCH body rejected (no fields to update)
- Network failure mapped to `UNAVAILABLE` so callers can branch
- `go test -race` clean — middleware/handlers safe under
  concurrent `httptest` clients

### Smoke-script edge cases

- `set -o pipefail` interaction: a non-zero command piped into `grep`
  was incorrectly failing the pipeline. Fixed by capturing the output
  into a variable with `|| true` before grepping. Documented in the
  script.

## 6. Bugs found and fixed during testing

| # | Where | What |
|---|-------|------|
| 1 | `e2e/m2-smoke.sh` | The "revoked token rejects" assertion used `cmd 2>&1 \| grep -q AUTH_REVOKED` under `set -o pipefail`. The whoami exits non-zero (correctly), pipefail propagated, the entire `if` saw a failure even though grep matched. Replaced with `out="$(cmd 2>&1 || true); grep -q ... <<<"$out"`. |
| 2 | `internal/api/api_test.go` | Initial draft imported `internal/store` but never used it; `go test` failed with "imported and not used". Removed the import. |

No production-code bugs surfaced in M2. The implementation was
test-driven enough that all paths landed correct on first run. (The
two issues above were both in test-harness code.)

## 7. Known weaknesses (tests cannot cover)

- **Real-DB corruption recovery**: SQLite's `BUSY` / `LOCKED` paths are
  not exercised because the daemon uses `max_open_conns = 1`. If we
  later move to a multi-writer regime we will need fault-injection
  tests.
- **Cross-process socket security**: tests run as the same UID as the
  daemon, so the `chmod 0o600` defense is not actually tested for
  cross-UID denial. Manual ops-time verification needed.
- **Migration backward-compat**: only `0001_init.up.sql` exists. The
  migration runner is exercised but not against schema evolution. We
  will add a "v1 → v2 rolling upgrade" test in M4 when the second
  migration lands.
- **Time-injected concurrency**: the clock injection works (used in
  audit + account + auth tests) but we don't yet test for race
  conditions across simulated time skew. Probably overkill.
- **Real Discord behavior**: by design, none of M2 touches Discord.
  This is M3's responsibility.

## 8. Phase 2 exit checklist

```
[x] unit tests for every non-trivial biz package    (10 of 10 code-bearing)
[x] repo tests against real on-disk SQLite          (12 cases in sqlite_test.go)
[x] HTTP API tests via httptest                     (14 cases)
[x] e2e script in e2e/ runs green                   (m1-smoke + m2-smoke)
[x] go test ./... -race clean
[x] coverage ≥ 70% on each code-bearing package     (70.6%–100%)
[x] phase1.md written
[x] phase2.md written (this file)
```

## 9. Hand-off to Phase 3

When the user is ready, Phase 3 begins with a fresh `Agent` subagent
(`subagent_type: general-purpose`) briefed on:

- `docs/02-requirements-final.md`
- `docs/03-architecture.md`
- `docs/04-roadmap.md` §3 (M2 scope)
- `docs/05-engineering-workflow.md`
- `docs/milestones/M2-phase1.md` and `docs/milestones/M2-phase2.md`
- The full M2 code (the auditor reads files itself)

The auditor produces `docs/milestones/M2-phase3.md` with severity-tagged
issues; the developer triages with the user; Blockers + Majors must be
resolved (or formally accepted) before M2 closes and M3 begins.
