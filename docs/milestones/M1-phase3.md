# M1 — Phase 3 Report (External Audit)

**Date:** 2026-05-13  
**Auditor role:** review / test engineer  
**Conclusion:** **PASS with minor non-blocking issues**

## 1. Scope

Reviewed M1 against:

- `docs/02-requirements-final.md`
- `docs/03-architecture.md`
- `docs/04-roadmap.md` §2 M1
- `docs/05-engineering-workflow.md`
- `docs/milestones/M1-phase1.md`
- `docs/milestones/M1-phase2.md`

M1 scope is repository skeleton only: Go module, two cobra binaries,
placeholder package layout, Makefile, README, `.gitignore`, license, and
smoke/test wiring. No business behavior, daemon runtime, database, auth,
Discord, API, or state engine is expected yet.

## 2. Extra Audit Tests Added

I added precise unit coverage for cobra's built-in `--version` flag in:

- `cmd/agentchat/cmds/version_test.go`
- `cmd/agentchatd/cmds/version_test.go`

Reason: Phase 2 smoke-tested `--version`, but unit tests only locked the
`version` subcommand. The flag is part of the M1 demo contract, so it is now
covered hermetically as well.

## 3. Verification Run

Command:

```bash
make fmt vet test-race smoke cover
```

Result: **PASS**

Observed results:

- `go fmt ./...`: clean
- `go vet ./...`: clean
- `go test -race ./...`: pass, no race warnings
- `e2e/m1-smoke.sh`: pass
- Coverage:
  - `cmd/agentchat/cmds`: 75.0%
  - `cmd/agentchatd/cmds`: 75.0%
  - project total: 33.3%

Note: the first sandboxed run failed because Go tried to write build cache
under `/home/weiland/.cache/go-build`, which was read-only in the sandbox.
The same command passed when rerun with the required filesystem permission.
This is an environment restriction, not a project failure.

## 4. Findings

### Blocker

None.

### Major

None.

### Minor

#### M1-P3-001: Error path prints an extra blank stderr line

- Severity: Minor
- Files:
  - `cmd/agentchat/main.go:17`
  - `cmd/agentchatd/main.go:16`

`SilenceErrors` is currently `false`, so cobra already prints the command
error. `main()` then calls `fmt.Fprintln(os.Stderr)` with no content, adding a
blank stderr line before exiting.

Why it matters: the project is agent-first, so stderr should stay as stable
and minimal as possible. This is not blocking for M1 because the required
happy-path commands all pass, but M2 should centralize CLI error formatting
when structured error codes are introduced.

Recommendation: either remove the blank `Fprintln`, or set
`SilenceErrors: true` and print the final error in exactly one place.

### Nit / Process

#### M1-P3-002: Dependency documentation audit trail is not fully reproducible

- Severity: Nit
- File: `docs/milestones/M1-phase1.md`

Phase 1 says cobra's GitHub README and user guide were checked before adding
the dependency. That satisfies the intent of the workflow, but the report does
not include exact URLs or access date.

Why it matters: future audits are easier if dependency decisions can be
replayed without guessing which page was consulted.

Recommendation: for future milestones, record the exact official doc URL and
the version checked when adding or upgrading third-party packages.

## 5. Questions / Doubts

1. Should M2 switch cobra root commands to `SilenceErrors: true` so the CLI can
   fully own structured error rendering and exit-code mapping?
2. Should coverage reporting continue to use project-total coverage, or should
   reports focus on code-bearing packages until internal packages gain real
   implementation? M1's 33.3% total is expected but not very informative.

## 6. Architecture / Workflow Compliance

- Module path is `github.com/LinZiyang666/agentchat`, matching the architecture.
- The `cmd/`, `internal/`, and `pkg/client` skeleton matches the M1 roadmap.
- Only cobra was added as a direct third-party dependency.
- No business package imports concrete Discord or SQLite implementation code.
- Package comments and exported Go symbols are documented in English.
- `make build`, `--help`, `version`, `--version`, `go test`, race test, and smoke
  all pass after the added audit tests.

## 7. Final Decision

**M1 Phase 3 passes.**

The current implementation satisfies M1's repository-skeleton acceptance
criteria. The minor stderr formatting issue should be handled during M2's
structured error work, but it does not block advancing from M1 to M2.

---

## 8. Triage decisions (added 2026-05-13 after review with user)

| Issue | Severity | Decision | Carrier |
|-------|---------|----------|---------|
| **M1-P3-001** stderr blank line on error path | Minor | **Defer to M2.** Will be fixed when `internal/errcode/` lands. M2 will set `SilenceErrors: true` on both root commands and centralize error formatting in one place (single `fmt.Fprintln(os.Stderr, formatErr(err))` + exit-code mapping). | M2 backlog |
| **M1-P3-002** dependency-doc audit trail missing URL/date | Nit | **Accept recommendation going forward.** Retroactively fixing M1's report is low value; from M2 onward, every dependency add/upgrade in `M<N>-phase1.md` will include: (a) exact official doc URL consulted, (b) version checked, (c) check date. Workflow doc updated to reflect this. | Workflow §1.8 |

### Open questions from §5 — answered

- **Q1** "Should M2 switch to `SilenceErrors: true`?" → **Yes.** Aligned with the M1-P3-001 fix path. Recorded as M2 design intent.
- **Q2** "Coverage reporting focus?" → **Yes.** From M2 onward, `M<N>-phase2.md` will report per-package coverage on code-bearing packages and explicitly note placeholder packages as out-of-scope, instead of headlining the diluted project-total figure.

### Workflow doc patch

The two answers above motivate a small edit to `docs/05-engineering-workflow.md` §1.8 to require URL + date when documenting third-party dependency additions, and a clarification in the Phase 2 checklist that coverage targets apply to code-bearing packages. These edits are tracked as a side effect of M1 closeout, not as M1 scope.

### Status

**M1 is closed.** Ready to begin M2 once user gives the "go".
