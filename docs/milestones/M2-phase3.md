# M2 — Phase 3 Report (External Audit)

**Date:** 2026-05-13  
**Auditor role:** review / test engineer  
**Conclusion:** **FAIL — Blocker + Major issues found**

M2's happy path is functional, and the daemon/API/CLI surface is much more
complete than M1. However, Phase 3 found one daemon lifecycle blocker and
multiple M2-scope correctness/security gaps. M2 should not close until the
Blocker and Major findings are fixed or explicitly accepted.

## 1. Scope

Reviewed M2 against:

- `docs/02-requirements-final.md`
- `docs/03-architecture.md`
- `docs/04-roadmap.md` §3 M2
- `docs/05-engineering-workflow.md`
- `docs/milestones/M2-phase1.md`
- `docs/milestones/M2-phase2.md`
- Full M2 code

Expected M2 surface: daemon startup, SQLite v1 migration, root bootstrap,
Unix socket, account CRUD, token auth, admin RBAC, audit log, CLI commands,
TTY/JSON output, and M2 smoke.

## 2. Verification

### Baseline verification

- `go test -race ./...`: **PASS**
- `./e2e/m2-smoke.sh`: **PASS** when run outside the sandbox, where Unix
  socket creation is permitted.
- Code-bearing coverage command:

```bash
go test -coverprofile=/tmp/agentchat-code-cover.out \
  ./internal/account ./internal/api ./internal/audit ./internal/auth \
  ./internal/cliutil ./internal/config ./internal/crypto ./internal/errcode \
  ./internal/store/sqlite ./pkg/client
```

Result: **PASS**, total 82.6%, package-level numbers match Phase 2.

### Full quality gate reproducibility

Command:

```bash
make fmt vet test-race smoke cover
```

Result in this audit environment: **FAIL at `make cover` only**.

`fmt`, `vet`, `test-race`, `m1-smoke`, and `m2-smoke` pass. The failure is:

```text
go test -coverprofile=coverage.txt ./...
go: no such tool "covdata"
```

Observed toolchain:

```text
go version go1.25.0 linux/amd64
GOTOOLDIR=/home/weiland/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64/pkg/tool/linux_amd64
```

This appears toolchain/Makefile related rather than a business-test failure,
but it means I could not reproduce the exact advertised quality gate end to
end in this environment.

### Extra audit probes

I temporarily added failing audit tests, then removed them so the existing
tree is not left with intentionally failing tests. They confirmed:

- existing broad data-root permissions are not tightened to `0700`;
- TOML `data_root` changes do not rebase default socket/db/key paths;
- `PATCH /v1/accounts/{id}` can partially rename an account before returning
  a role validation error;
- rename-only account updates do not create audit entries.

I also ran CLI/daemon probes:

- `agentchat admin account create` with no `--name` exits `0`;
- `agentchat admin account rename <id>` with no `--name` exits `0`;
- two `agentchatd serve --data-root <same-dir>` processes can run at once.

## 3. Findings

### Blocker

#### M2-P3-001: No data-root lock; a second daemon can start on the same DB/socket

- Severity: **Blocker**
- Files:
  - `cmd/agentchatd/cmds/serve.go:96`
  - `cmd/agentchatd/cmds/serve.go:194`
- Evidence: escalated probe showed `p1=alive p2=alive` after starting two
  daemons with the same `--data-root`.

`runServe` removes any existing socket file and then binds a new listener, but
there is no data-root lock. A second daemon can unlink the active socket path
and start another HTTP server against the same SQLite database.

Why it matters: the architecture assumes one daemon owns the local cache,
state, and IPC endpoint. Two daemons make request routing undefined and can
split in-memory auth/state assumptions once M3+ add bot connections and M5
adds state aggregation.

Recommendation: add an exclusive lock file under the data root before opening
SQLite or touching the socket. Refuse startup if another daemon owns the lock.
Also avoid blindly unlinking an active socket; probe/connect or pair socket
cleanup with the lock owner.

### Major

#### M2-P3-002: Account PATCH is not atomic; an error response can still mutate data

- Severity: **Major**
- File: `internal/api/v1/accounts.go:85`

`UpdateAccount` applies rename first, then role change. If a request contains
both `name` and an invalid `role`, the handler returns `400 INVALID_ARGUMENT`
but the account has already been renamed.

Audit test result:

```text
expected name "atomic-old"; actual "atomic-new"
```

Recommendation: validate all requested fields before mutating, then apply the
update through one service method/repository transaction.

#### M2-P3-003: Account rename is a mutating admin operation but is not audited

- Severity: **Major**
- Files:
  - `internal/api/v1/accounts.go:85`
  - `internal/api/v1/accounts.go:98`
  - `docs/04-roadmap.md:112`

M2 requires admin operations to land in `audit_log`. Role changes are audited,
but rename-only updates skip audit entirely.

Audit test result:

```text
audit entry count before rename == audit entry count after rename
```

Recommendation: add `account.rename` (or `account.update`) and record old/new
name in the payload.

#### M2-P3-004: Audit writes are best-effort and errors are swallowed

- Severity: **Major**
- Files:
  - `internal/api/v1/accounts.go:98`
  - `internal/api/v1/accounts.go:123`
  - `internal/api/v1/tokens.go:32`
  - `internal/api/v1/tokens.go:74`

Handlers ignore `recorder.Record` errors. If audit insertion fails, the admin
mutation still succeeds with no durable audit trail.

Why it matters: M2's audit log is the accountability mechanism for admin
operations. If it is mandatory, failures must either fail the operation or be
explicitly downgraded in the design.

Recommendation: decide policy. For security/accountability, fail closed on
audit write failure for mutating admin operations.

#### M2-P3-005: Existing data-root permissions are not tightened

- Severity: **Major**
- File: `internal/config/config.go:156`

`EnsureDataRoot` calls `os.MkdirAll(..., 0700)`, but `MkdirAll` does not change
permissions for an existing directory. If the operator points `AGENTCHAT_HOME`
or `--data-root` at an existing `0755` directory, the daemon leaves it broad.

Why it matters: SQLite DB files are created with normal process umask, and in
my probe the DB file was `0644`. A broad data root can expose account metadata,
token hashes, future attachment indexes, and encrypted bot-token material.

Recommendation: after `MkdirAll`, `Stat` and `Chmod` the data root to `0700`
or refuse to run if it is too open. Consider `0600` for DB files as defense in
depth.

#### M2-P3-006: TOML `data_root` does not rebase default derived paths

- Severity: **Major**
- Files:
  - `internal/config/config.go:75`
  - `internal/config/config.go:137`

The comment says derived paths are refreshed when `DataRoot` moves, but the
defaults are already absolute before TOML overlay. A config file that sets only
`data_root = "/new/root"` leaves `SocketPath`, `DBPath`, and `KeyPath` pointing
at the old root.

Audit test result:

```text
expected /next/agentchatd.sock; actual /base/agentchatd.sock
```

Recommendation: track whether socket/db/key were explicitly set, or apply the
TOML overlay before deriving default paths.

#### M2-P3-007: Missing required CLI flags return success

- Severity: **Major**
- File: `cmd/agentchat/cmds/admin_account.go:24`
- File: `cmd/agentchat/cmds/admin_account.go:127`

Both `admin account create` without `--name` and `admin account rename <id>`
without `--name` call `cmd.Help()` and return nil. The process exits `0`.

Why it matters: the CLI is agent-first. A missing required argument must be a
machine-detectable failure, ideally `INVALID_ARGUMENT` / exit 22, not a
successful help render.

Recommendation: use cobra required flags or return `errcode.InvalidArgument`
explicitly.

#### M2-P3-008: `~/.agentchat/cli.toml` token source is missing from M2 scope

- Severity: **Major**
- Files:
  - `docs/04-roadmap.md:115`
  - `cmd/agentchat/cmds/root.go:71`

M2 scope requires token precedence:

```text
--token > AGENTCHAT_TOKEN > ~/.agentchat/cli.toml
```

The implementation supports only flag/env and documents `cli.toml` as planned
for M8. That is a scope deviation unless the deferral is formally accepted.

Recommendation: either implement CLI config-token loading in M2, or update the
roadmap/milestone acceptance criteria to record the deferral.

#### M2-P3-009: Advertised `make cover` gate is not reproducible here

- Severity: **Major**
- Files:
  - `Makefile:27`
  - `go.mod:3`

`make cover` runs `go test -coverprofile=coverage.txt ./...`. In this audit
environment, Go 1.25.0's auto toolchain does not contain `covdata`, causing
the exact quality gate to fail even though code-bearing package coverage
passes.

Recommendation: either pin/verify a complete toolchain, add a `toolchain`
directive that matches CI, or make `cover` target the code-bearing package
list used by Phase 2 reporting.

### Minor

#### M2-P3-010: README is stale after M2

- Severity: **Minor**
- File: `README.md:14`
- File: `README.md:25`
- File: `README.md:40`

README still says M1 is current, Go 1.22+ is enough, and smoke is M1 only.
`go.mod` now requires Go 1.25.0 and `make smoke` runs both M1 and M2 scripts.

Recommendation: update README during M2 closeout.

#### M2-P3-011: CLI error output hides wrapped causes

- Severity: **Minor**
- File: `internal/cliutil/cliutil.go`

For `*errcode.Error`, `PrintError` prints only `Code` and `Message`, not the
wrapped cause. During sandbox diagnosis this reduced a useful OS error
(`operation not permitted` on Unix socket bind) to:

```text
Error [INTERNAL]: listen on /path/agentchatd.sock
```

Recommendation: for humans/logs, include the wrapped cause behind a debug flag
or in daemon stderr logs while keeping the structured code stable.

## 4. Questions

1. Is the audit log intended to cover every admin HTTP request, or only
   mutating admin operations? The roadmap says "所有 admin 操作"; the current
   implementation audits only some mutations.
2. Should the system prevent deleting/demoting the last admin or deleting the
   currently authenticated account? The requirements give admin full power, but
   the current behavior can lock out the running daemon until restart/bootstrap.
3. Is `~/.agentchat/cli.toml` still a hard M2 acceptance item, or is the M8
   deferral accepted?

## 5. Positive Notes

- The service/repository/API layering is generally clean; business packages do
  not import SQLite or Discord concrete packages.
- Auth error mapping avoids token-ID `NOT_FOUND` leakage.
- Raw tokens are only emitted on creation; token list responses omit hashes and
  plaintext.
- The real Unix-socket client tests and `e2e/m2-smoke.sh` provide useful
  coverage of the actual daemon path.
- Code-bearing package coverage is above the 70% target.

## 6. Final Decision (initial audit)

**M2 Phase 3 does not pass.**

Minimum closeout recommendation:

1. Fix the daemon single-instance/data-root lock.
2. Fix atomic account update and rename auditing.
3. Enforce data-root permissions and resolve TOML `data_root` path rebasing.
4. Make missing CLI required flags return non-zero structured errors.
5. Decide and document whether `cli.toml` is M2 or a formally accepted deferral.
6. Make the documented quality gate reproducible in the target toolchain.

---

## 7. Triage decisions and remediation (added 2026-05-13 after review with user)

> 🧭 **Reader note** — §10 below supersedes the resolved-state wording in §7
> and §9 (M2-P3-013 follow-up). §7.1 entries that mention `AuditOrFail` are
> first-round patches that were later replaced by `WithTx` + `RecordVia`; §9
> is the second-round conditional-fail snapshot before M2-P3-012 was fixed.
> The current authoritative outcome is in §10.

User decision: **fix every Blocker + Major + Minor**; close M2 only after a
re-verified green run.

### 7.1 Resolutions

| Issue | Severity | Resolution |
|-------|---------|------------|
| **M2-P3-001** No data-root lock | Blocker | **Fixed.** `cmd/agentchatd/cmds/datalock.go` adds `acquireDataRootLock` using `syscall.Flock(LOCK_EX|LOCK_NB)` on `<DataRoot>/agentchatd.lock`. `serve.go` now takes the lock *before* touching socket or DB; second daemon on same root is rejected with `errcode.Conflict`. Unit test `TestAcquireDataRootLockRejectsSecond` + e2e assertion in `m2-smoke.sh`. |
| **M2-P3-002** PATCH non-atomic | Major | **Fixed.** New `account.Service.Update(ctx, id, UpdateRequest)` validates every requested field before any DB write. The HTTP handler in `accounts.go` UpdateAccount uses it; `Rename`/`SetRole` are now thin wrappers. Unit test `TestUpdateAtomicityRejectsBeforeWriting` + HTTP-level `TestPATCHAtomicityInvalidRoleNoRename`. |
| **M2-P3-003** Rename not audited | Major | **Fixed.** PATCH emits one `account.update` audit row carrying the full list of changed fields (`[{field, old, new}, ...]`). No-op updates skip both the DB write and the audit row. Tests: `TestPATCHRenameIsAudited`, `TestPATCHNoopProducesNoAudit`. New `audit.ActionAccountUpdate` constant; legacy `account.set_role` constant retained for downstream readers. |
| **M2-P3-004** Audit errors swallowed | Major | **Fixed (superseded by M2-P3-012 architectural fix).** First-round patch added `apiv1.AuditOrFail` that wrote 500 on audit failure — but the mutation had already committed. Second-round Major M2-P3-012 (see §7.2b) moved every mutating handler to `store.Bundler.WithTx` + `Recorder.RecordVia` so the mutation and audit insert share one transaction. `AuditOrFail` now exists only as a deprecated helper for future non-mutating bookkeeping; no current handler calls it. `TestAuditFailureRollsBackMutation` verifies the new contract. |
| **M2-P3-005** Wide-perm data root not tightened | Major | **Fixed.** `Config.EnsureDataRoot` now calls `os.Chmod(DataRoot, 0o700)` after `MkdirAll`, so pre-existing wide-perm dirs are explicitly narrowed. Unit test `TestEnsureDataRootTightensExistingDir`. |
| **M2-P3-006** TOML `data_root` does not rebase | Major | **Fixed.** `config.Load` now reads the TOML overlay first, decides the *effective* data root (TOML wins over CLI/env if it differs), recomputes defaults against that root, and only then applies explicit non-empty TOML overrides on top. Tests: `TestLoadTOMLDataRootRebasesPaths`, `TestLoadExplicitTOMLSocketOverridesRebase`. |
| **M2-P3-007** Missing CLI required flags exit 0 | Major | **Fixed.** Both `admin account create` and `admin account rename` use `cmd.MarkFlagRequired("name")`; cobra returns a non-zero error with a usage hint, and `cliutil.PrintAndExit` propagates exit code 1. E2E assertion added to `m2-smoke.sh`. |
| **M2-P3-008** `cli.toml` token source missing | Major | **Fixed.** `cmd/agentchat/cmds/root.go` adds `tokenFromConfigFile` reading `<data-root>/cli.toml`'s `token = "..."` entry; help text updated. Tests in `cmd/agentchat/cmds/root_test.go`: flag-wins, env-beats-file, file-source, missing-file, malformed-file. E2E assertion in `m2-smoke.sh`. |
| **M2-P3-009** `make cover` not reproducible | Major | **Fixed.** `Makefile` now declares `COVER_PKGS` (the code-bearing list) and `make cover` runs only against those packages. Sidesteps the `covdata` toolchain quirk and aligns the gate with workflow §1.6's "code-bearing packages only" rule. |
| **M2-P3-010** README stale | Minor | **Fixed.** README now mentions M2 as the latest closed milestone, Go 1.25 minimum, and smoke covering both M1+M2. |
| **M2-P3-011** Hidden wrapped cause | Minor | **Fixed.** `cliutil.PrintError` now appends `Caused by: <cause>` when an `*errcode.Error` wraps another error, restoring the OS-error trail behind the structured code. |
| **M2-P3-012** (second-round) Mutation persists without audit | Major | **Fixed.** Mutation + audit insert now share a single SQLite transaction via the new `store.Bundler.WithTx` seam. A failed audit rolls the mutation back, so there is no "audited but happened" state. Details in §7.2b. |

### 7.2 Additional fix surfaced during remediation

While fixing M2-P3-003 the new HTTP test exposed a deeper SQL ordering bug:
`audit_repo.List` used `ORDER BY created_at DESC` only. Two entries written
in the same wall-clock second came back in an undefined order, making
"newest first" non-deterministic. Fixed by adding `id DESC` as a secondary
sort: since UUIDv7 is time-ordered, the lexicographically larger id wins
ties. Captured in this resolution log so it does not disappear into the
diff.

### 7.2b M2-P3-012 — transactional mutation+audit (second-round Major)

The second-round audit flagged that **M2-P3-004 was only half-fixed**:
`AuditOrFail` did return a 500 on audit failure, but the mutation had
already been committed to the DB before the audit insert was attempted.
A broken audit repo left an *orphan* mutation (account exists, no audit
row).

**Resolution: option 1 (transactional fix).** User picked the
architectural answer over deferring the risk.

Changes:

- `internal/store/store.go` — new `Bundler` interface:
  `WithTx(ctx, fn(b Bundle) error) error`. Backends implement it.
- `internal/store/sqlite/db.go` — new `queryExecer` interface (the
  subset of `*sql.DB` / `*sql.Tx` the repos use). New `Store.WithTx`
  begins a SQLite transaction, builds a `Bundle` whose repos write
  through that tx, runs the closure, and commits or rolls back as one
  unit.
- `internal/store/sqlite/{account,token,audit}_repo.go` — `db` field
  is now of type `queryExecer` instead of `*sql.DB`. Zero changes to
  call sites; same code now runs against either handle.
- `internal/audit/audit.go` — new `Recorder.RecordVia(ctx, repo, ...)`
  that writes through an explicitly-provided `AuditRepo`. `Record`
  delegates to `RecordVia(r.repo, ...)`.
- `internal/auth/token.go` — new `Manager.IssueVia` and `RevokeVia`
  with the same "use this repo" override pattern; `Issue` and `Revoke`
  delegate.
- `internal/account/service.go` — new `Build` (validate + construct
  without DB I/O) and `PlanUpdate` (validate + plan changes against an
  existing account, no DB I/O). `Create` and `Update` are now thin
  wrappers that combine the pure half with a `repo.Create`/`.Update`.
- `internal/api/server.go` — `Deps.Bundler store.Bundler` added;
  handlers are constructed against it.
- `internal/api/v1/accounts.go` — `CreateAccount`, `UpdateAccount`,
  `DeleteAccount` all wrap their mutation + audit insert in
  `bundler.WithTx`. The handler signatures changed accordingly.
- `internal/api/v1/tokens.go` — `CreateToken` and `RevokeToken` same
  treatment.
- `internal/api/v1/helpers.go` — `AuditOrFail` is now documented as
  deprecated for mutating paths; retained only as a non-mutating
  bookkeeping helper.
- `cmd/agentchatd/cmds/serve.go` — wires `Bundler: db` (the `*sqlite.Store`
  satisfies the interface).

Verification (new test `TestAuditFailureRollsBackMutation`):

- Build a router whose `Bundler` is `brokenAuditBundler`: forwards
  `WithTx` to a real `*sqlite.Store` but injects `brokenAuditRepo` as
  `bundle.Audit` so any `RecordVia` call fails.
- `POST /v1/accounts {name: "ghost"}` → response 500 ✅
- `bundle.Accounts.GetByName(ctx, "ghost")` → `NOT_FOUND` ✅
  (transactional rollback erased the orphan account row)

Concurrency note: SQLite serialization (`max_open_conns = 1`) means
`WithTx` holds the single connection for the duration of the closure.
Other handlers queue. For our scale this is fine; M5+ may want a read
pool but that is out of scope.

### 7.3 Open auditor questions — answered

1. **"Audit every admin or only mutating?"** → Only **mutating** admin
   operations. GETs (list/get accounts, list tokens, list audit) carry no
   audit row. The current implementation matches this policy: every
   `account.{create,update,delete}` and `token.{create,revoke}` mutation
   path goes through `AuditOrFail`.
2. **"Should we forbid deleting/demoting the last admin?"** → Deferred.
   This is a useful invariant but is outside M2 scope and the
   requirements document does not mandate it. Filed mentally as an M8
   safety-net follow-up; not blocking M2 closeout.
3. **"Is `cli.toml` an M2 hard requirement?"** → It was. Resolved by
   implementing the source (M2-P3-008 fix).

### 7.4 Re-verification

After the fixes:

- `go test -race ./...` — to be re-run in §7.5 below.
- `./e2e/m1-smoke.sh` + `./e2e/m2-smoke.sh` — both green; m2-smoke now
  exercises the lock conflict, MarkFlagRequired path, and cli.toml token
  source.
- `make cover` — runs against `COVER_PKGS`; reproducible without
  depending on the `covdata` tool.

### 7.5 Re-verification outcome (after the second-round M2-P3-012 fix)

Quality gate re-run after every fix landed:

| Check | Result |
|------|--------|
| `make fmt` | clean (no diff) |
| `make vet` | clean |
| `make test-race` | **PASS** — no race warnings, ~250s wall-clock (bcrypt-12 dominated) |
| `make smoke` | **PASS** — `m1-smoke` + `m2-smoke` both green; `m2-smoke` asserts data-root lock (B1), required-`--name` flag (M6), and `cli.toml` token source (M7) |
| `make cover` | **PASS** — see table below |

Coverage (post-fix, code-bearing packages, code-bearing-only list):

| Package | Lines | Note |
|---------|------|------|
| internal/account | **90.4%** | `Build` / `PlanUpdate` paths covered |
| internal/api | **100.0%** | full handler coverage incl. broken-audit-bundler injection |
| internal/audit | **84.2%** | new `RecordVia` covered by integration tests; default `Record` path now alternate-only |
| internal/auth | **83.0%** | new `IssueVia` / `RevokeVia` covered indirectly via API tests |
| internal/cliutil | **73.7%** | Caused-by branch covered |
| internal/config | **84.7%** | finalize relative-path branch is the residue |
| internal/crypto | **79.4%** |  |
| internal/errcode | **95.3%** |  |
| internal/store/sqlite | **74.0%** | `WithTx` happy-path covered via every mutating API test; rollback path covered by `TestAuditFailureRollsBackMutation` |
| pkg/client | **80.2%** |  |
| cmd/agentchat/cmds | 21.3% | binary entry point — covered end-to-end via `m2-smoke.sh`; unit tests target helpers |
| cmd/agentchatd/cmds | 28.1% | same — `serve` orchestrator covered by `m2-smoke.sh` |
| **total** | **72.6%** | |

All 10 business packages exceed the 70% gate. The two `cmd/*/cmds`
packages are deliberately below the gate by policy: their helpers are
unit-tested and their orchestrators are end-to-end-tested. This is the
same justification the workflow doc allows for "explained" coverage.

## 8. Status

**M2 Phase 3 — PASS (after fixes)** — Blocker resolved, all 8
first-round Majors resolved, both first-round Minors resolved, the
second-round Major (M2-P3-012, transactional mutation+audit) resolved
with an architectural seam (`store.Bundler.WithTx`), plus a deeper
audit-list ordering bug surfaced during remediation and fixed. M2
ready to close.

---

## 9. Second-round audit (2026-05-13)

Requested after remediation. This pass re-read the critical fixes instead of
trusting §7, then re-ran the quality gate.

### 9.1 What I re-checked

- M2-P3-001 data-root lock: `serve.go` now acquires
  `<DataRoot>/agentchatd.lock` before master key, SQLite, bootstrap, and socket
  cleanup/listen. `m2-smoke.sh` also asserts that a second daemon returns
  `CONFLICT`.
- M2-P3-002 atomic PATCH: `account.Service.Update` validates all requested
  fields before repository mutation; HTTP-level regression test covers
  valid-name + invalid-role.
- M2-P3-003 rename audit: PATCH writes `account.update` with changed fields;
  no-op PATCH skips audit.
- M2-P3-005 / M2-P3-006 config fixes: existing data-root perms are chmodded to
  `0700`; TOML `data_root` rebases default socket/db/key paths.
- M2-P3-007 / M2-P3-008 CLI fixes: required `--name` is enforced by cobra;
  `cli.toml` token source is implemented and e2e-covered.
- M2-P3-009 coverage: `make cover` now targets code-bearing packages and is
  reproducible in this environment.

### 9.2 Extra test added in this audit

Added `TestAuditListOrdersSameSecondByIDDescending` in
`internal/store/sqlite/sqlite_test.go` to lock the same-second audit ordering
fix. The test passed.

### 9.3 Verification outcome

Command:

```bash
make fmt vet test-race smoke cover
```

Result: **PASS**

Observed coverage total: **73.4%**. Code-bearing packages remain above the
70% gate; `internal/store/sqlite` rose to **77.5%** after the added ordering
test.

### 9.4 Remaining issue

#### M2-P3-012: Audit failure still leaves the mutation persisted

- Severity: **Major / requires explicit acceptance**
- Files:
  - `internal/api/v1/helpers.go:134`
  - `internal/api/v1/accounts.go:20`
  - `internal/api/v1/tokens.go:20`

`AuditOrFail` now returns HTTP 500 when `recorder.Record` fails, so the client
does not see a successful admin operation. However, the business mutation has
already been committed by the time audit recording happens. I confirmed this
with a temporary probe: a broken audit repo returns 500 for account creation,
but the account is still present in the account repository afterward.

This means M2-P3-004 is only partially fixed. The system no longer silently
succeeds, but it can still create an unaudited admin mutation, which conflicts
with the accountability goal behind "mutating admin operations land in
audit_log".

Recommendation:

1. Preferred: make mutation + audit one transaction before closing M2.
2. Acceptable only if explicitly signed off: keep the current 500 behavior and
   record this as a known M2 limitation to be fixed when transaction support is
   introduced.

### 9.5 Second-round decision

**M2 Phase 3 second-round status: CONDITIONAL FAIL.**

All original findings except M2-P3-004 are resolved in code and verified. The
quality gate is green. M2 can close only if M2-P3-012 is either fixed or
explicitly accepted as a deferred transactional-audit limitation.

---

## 10. Third-round re-audit (2026-05-13, latest tree)

User requested a fresh re-check after another developer revision. This section
supersedes the second-round conditional failure in §9; §9 is retained only as
historical audit trail.

### 10.1 Conclusion

**M2 Phase 3 latest status: PASS.**

The previous blocker, **M2-P3-012**, is now fixed in code and covered by a
regression test. Mutating admin operations no longer have a path where the
business mutation commits but the audit insert fails afterward.

### 10.2 What changed since §9

- `store.Bundler.WithTx(ctx, fn)` was added as the transaction seam.
- `internal/store/sqlite.Store.WithTx` opens one SQLite transaction and passes
  tx-backed account/token/audit repos to the closure.
- Account mutations (`POST/PATCH/DELETE /v1/accounts...`) now call
  `Recorder.RecordVia` inside the same `WithTx` closure as the account write.
- Token mutations (`POST /v1/accounts/{id}/tokens`,
  `DELETE /v1/tokens/{id}`) now call `IssueVia` / `RevokeVia` and
  `RecordVia` inside the same transaction.
- `TestAuditFailureRollsBackMutation` injects a broken tx-scoped audit repo and
  verifies that `POST /v1/accounts` returns 500 and leaves no `ghost` account
  in the database.

### 10.3 Tests I ran

Targeted regression tests:

```bash
go test ./internal/api -run 'TestAuditFailureRollsBackMutation|TestPATCHAtomicityInvalidRoleNoRename|TestPATCHRenameIsAudited|TestPATCHNoopProducesNoAudit|TestTokenLifecycle' -count=1
go test ./internal/store/sqlite -run 'TestAuditListOrdersSameSecondByIDDescending|TestForeignKeyCascadeOnAccountDelete' -count=1
```

Result: **PASS**.

Full quality gate:

```bash
make fmt vet test-race smoke cover
```

Result: **PASS**.

Current coverage output from `make cover`:

| Package | Coverage |
|---------|----------|
| internal/account | 90.4% |
| internal/api | 100.0% |
| internal/audit | 84.2% |
| internal/auth | 83.0% |
| internal/cliutil | 73.7% |
| internal/config | 84.7% |
| internal/crypto | 79.4% |
| internal/errcode | 95.3% |
| internal/store/sqlite | 74.0% |
| pkg/client | 80.2% |
| cmd/agentchat/cmds | 21.3% |
| cmd/agentchatd/cmds | 28.1% |
| total | 72.6% |

The 10 business packages remain above the 70% milestone gate. The two command
packages are still below 70%, but are covered through the smoke tests and
already documented as an explained exception.

### 10.4 Remaining non-blocking issues / suggestions

#### M2-P3-013: Historical report wording is stale

- Severity: **Minor / documentation**
- Location: §7.3 and §9 in this file

Some earlier text still describes the fixed state in terms of
`AuditOrFail`. Current code correctly uses `WithTx` + `RecordVia` for mutating
handlers, and `AuditOrFail` is now deprecated fallback code. This is not a code
risk, but it can confuse future readers because §9 says conditional fail while
§10 says pass.

Recommendation: when closing M2, add a short note near §7/§9 pointing readers
to §10 as the latest result, or leave the current audit-history shape and rely
on this section as the source of truth.

#### M2-P3-014: Token creation does bcrypt work while the DB tx is open

- Severity: **Minor / performance**
- Location: `internal/api/v1/tokens.go`, `internal/auth/token.go`
- Status: **Fixed during M2 closeout.**

`CreateToken` opens `WithTx`, then `IssueVia` generates the secret and runs
bcrypt before inserting the token row. Because the SQLite store currently uses
`max_open_conns=1`, this holds the single DB connection longer than necessary
during token creation.

This is acceptable for M2's local single-daemon scope and is not a correctness
blocker. A later cleanup can split token material generation from persistence:
prepare raw/id/hash before `WithTx`, then inside the transaction verify the
account still exists, insert the token row, and write audit.

Resolution: `auth.Manager.PrepareToken` now performs bcrypt and id/secret
generation with no I/O; `auth.Manager.PersistTokenVia` is a pure INSERT.
`CreateToken` calls `PrepareToken` before `WithTx` and `PersistTokenVia`
inside it. The tx now only contains the account existence check, the
fast INSERT, and the audit insert.

### 10.5 Final decision

**PASS.** M2 can close from the Phase 3 audit perspective. The only remaining
items are documentation cleanup and a future performance refinement; neither
blocks the milestone.
