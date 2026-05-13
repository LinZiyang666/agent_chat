# M1 — Phase 2 Report (Testing)

> Companion to `M1-phase1.md`. Records *how* the M1 deliverables were
> verified.

**Date:** 2026-05-13
**Test command summary:** `make fmt && make vet && make test-race && make smoke && make cover`
**Result:** all green.

## 1. What there is to test in M1

M1 is the repository skeleton — no business logic exists yet. The
testable surface is just the cobra wiring of two binaries' `version`
subcommands and one end-to-end build script.

Therefore the test discipline is:

- **Unit-test the only thing that has logic** (version subcommand)
  hermetically, with output captured into a buffer.
- **Smoke-test the build** via a shell script that runs the binaries
  exactly the way a human or agent would.
- **Leave a guard test** asserting that `rootCmd.Version` is non-empty,
  so a future refactor cannot silently disable `--version`.

Everything else (the 16 placeholder packages) has no executable
statements; their correctness is enforced by `go build ./...`, which
passes.

## 2. Test files added

| File | Tests |
|------|-------|
| `cmd/agentchat/cmds/version_test.go` | `TestVersionCommandPrintsVersion`, `TestRootCommandHasVersionField` |
| `cmd/agentchatd/cmds/version_test.go` | `TestVersionCommandPrintsVersion`, `TestRootCommandHasVersionField` |

### Why these specific tests

- **`TestVersionCommandPrintsVersion`** — locks in the exact output
  format (`"agentchat dev\n"` / `"agentchatd dev\n"`). The format is part
  of the project's external contract: agents and scripts parse it. A
  silent rename to e.g. `"v dev"` must fail this test.
- **`TestRootCommandHasVersionField`** — guards against the
  init-time assignment `rootCmd.Version = Version` being dropped or
  moved during a future refactor. Without `rootCmd.Version`, cobra
  silently disables the `--version` flag — a regression that would
  otherwise only surface when a user types `--version` and gets
  "unknown flag".

## 3. Test execution

### Race-detector run

```
$ go test -race ./...
ok  	github.com/LinZiyang666/agentchat/cmd/agentchat/cmds	1.010s
ok  	github.com/LinZiyang666/agentchat/cmd/agentchatd/cmds	1.009s
(16 placeholder packages: [no test files])
```

No failures, no race warnings.

### End-to-end smoke

```
$ ./e2e/m1-smoke.sh
==> make build
==> ./bin/agentchat --help
==> ./bin/agentchat version
==> ./bin/agentchat --version (cobra builtin)
==> ./bin/agentchatd --help
==> ./bin/agentchatd version
==> ./bin/agentchatd --version (cobra builtin)
OK: M1 smoke test passed
```

The script asserts on `version` subcommand output strings (`"agentchat
dev"` / `"agentchatd dev"`). It treats any non-zero exit from any
binary as a failure (set -e).

### Static checks

```
$ go fmt ./...        # no output (already formatted)
$ go vet ./...        # no output (no warnings)
```

## 4. Coverage

```
cmd/agentchat/cmds      75.0%
cmd/agentchatd/cmds     75.0%
(all internal/*)        no test files
(pkg/client)            no test files
project total           33.3%
```

### Reading the numbers

- **Per-package coverage on code-bearing packages: 75.0%**, exceeds the
  workflow's "≥ 70% on new biz code" threshold.
- **Uncovered 25% of `cmds`** is exclusively the cobra `init()` /
  `Execute()` glue, which runs on the import path of every other test
  anyway; explicit tests would be tautological.
- **Project-total 33.3%** is mathematically dominated by 16 packages
  containing only `doc.go` files (zero executable statements). This
  number is meaningless in M1 and will rise as packages gain code in
  later milestones.

## 5. Edge cases exercised

| Scenario | How exercised | Result |
|---|---|---|
| `version` subcommand with no args | unit test | prints expected line |
| `--version` flag (cobra built-in) | smoke script | exits 0 |
| `--help` flag (cobra built-in) | smoke script | exits 0 |
| Missing `rootCmd.Version` field | guard unit test | would fail loudly |
| Empty repo build | implicit (`make build` from clean) | both binaries produced |
| Race conditions in test execution | `go test -race` | clean |

## 6. Edge cases NOT exercised (deferred)

- **Cross-platform binary execution** (Windows / macOS): we run only on
  Linux/WSL right now. Cobra is cross-platform; revisit when distributing.
- **Stale `Version` after `-ldflags -X` override**: the override path
  works (cobra-standard), but we have no release pipeline yet to test
  it end-to-end. Pick up at M8.
- **Behavior under SIGINT / SIGTERM during command execution**: the M1
  commands all complete in microseconds; signal handling is irrelevant
  here but becomes important in M2 (long-running daemon).

## 7. Bugs found and fixed during testing

None. The implementation passed every test on first run.

The most likely class of bug at this stage (an off-by-one in the output
format) was prevented by writing the unit tests *immediately* after the
`Run` function — Phase 1 and Phase 2 interleaved as the workflow
specifies.

## 8. Known weaknesses tests cannot cover

- **Cobra-internal regressions**: if `cobra/v2` ever ships with a new
  CLI surface, our tests still pass against the old behavior. Mitigation:
  the dependency-discipline rule (§1.8) requires re-reading docs on
  every library upgrade.
- **`Makefile` correctness on systems with non-GNU make**: not tested.
  We assume GNU make (standard on Linux/macOS via homebrew, available
  in WSL).

## 9. Phase 2 exit checklist

```
[x] unit tests for every non-trivial biz package    (only cmds qualifies)
[x] repo tests against :memory: SQLite              (N/A in M1)
[x] HTTP API tests via httptest                     (N/A in M1)
[x] e2e script in e2e/ runs green
[x] go test ./... -race clean
[x] coverage ≥ 70% on new biz code (or explained)   (75% on cmds; total 33% explained)
[x] phase1.md written
[x] phase2.md written  (this file)
```

## 10. Hand-off to Phase 3

The user will (when ready) start Phase 3 by spawning an audit subagent
per `05-engineering-workflow.md` §Phase 3. The auditor should be briefed
with:

- `docs/02-requirements-final.md`
- `docs/03-architecture.md`
- `docs/04-roadmap.md` §2 (M1 scope)
- `docs/05-engineering-workflow.md`
- This Phase 1 report and Phase 2 report
- The current code (auditor reads files itself)

The auditor produces `docs/milestones/M1-phase3.md` with severity-tagged
issues. Triage with the user follows.
