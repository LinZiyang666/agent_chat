# Agent Chat — Engineering Workflow & Coding Rules v1.0

> This document defines **how code is written, tested, and audited** for this project. It applies to every milestone (M1–M8) defined in `04-roadmap.md`.
>
> Two parts:
> 1. **Coding rules** — what good code looks like here (decoupling, clarity, comments).
> 2. **Three-phase per-milestone workflow** — Implementation → Testing → External Audit, no exceptions.

---

## Part 1 — Coding Rules

### 1.1 Decoupling (most important)

**Rule:** Business logic must depend on **interfaces**, never on concrete external implementations.

Concrete obligations:

- `internal/bot/` defines a `Provider` interface; **every other package depends on `Provider`, not on `bot/discord/`**. Adding Matrix later must require zero changes outside `internal/bot/`.
- `internal/store/` defines repository interfaces (`AccountRepo`, `RoomRepo`, …); business packages depend on these, not on `sqlite/`.
- HTTP handlers depend on service interfaces; services depend on repository interfaces; **direct DB access from handlers is forbidden**.
- No package may import a package that sits "above" it in this layering:

```
cmd/         ──→  internal/api  ──→  internal/<biz>  ──→  internal/bot, internal/store, internal/crypto, internal/errcode
                                                       ──→  internal/state (consumer of biz events)
```

- Forbidden imports (CI should later enforce these via `depguard` or a linter rule, but enforce by review for now):
  - business packages → discordgo
  - business packages → modernc/sqlite
  - any package → `cmd/`

### 1.2 Logical Clarity

- **One function = one job.** If a function has an "and" in its name (`createRoomAndAddMembers`), split it.
- **Naming over comments.** A well-named symbol kills the need for a comment. Reach for renaming before reaching for a comment.
- **No magic numbers / strings.** Constants in the package they belong to.
- **Early return over nested if.** Guard clauses at the top, happy path stays at indent 1.
- **Errors must carry context.** `return fmt.Errorf("send message to room %s: %w", roomID, err)`. Never `return err` bare from a function that did meaningful work.
- **Context propagation.** Every function that does I/O takes `ctx context.Context` as the first arg. No exceptions.
- **Goroutines must have owners.** No "fire and forget". Each goroutine is started by a struct with a `Close()` / lifecycle that joins it.

### 1.3 Comments (English only)

**Default: write no comment.** Only add one when:
- The *why* is non-obvious (hidden constraint, subtle invariant, workaround for a known bug).
- A public exported symbol — must have a godoc-style comment starting with the symbol name.
- A non-trivial algorithm — a 1–3 line header explaining the approach (not the steps).

What comments must look like:

```go
// Good — explains WHY:
// Use bcrypt cost 12: high enough to slow brute force, low enough
// that CLI auth latency stays under 100ms on commodity hardware.
const bcryptCost = 12

// Good — godoc on exported:
// Provider abstracts a chat-platform backend (Discord, Matrix, …).
// All business code must depend on Provider, never on a concrete impl.
type Provider interface { ... }

// Bad — restates WHAT:
// increment i by 1
i++

// Bad — references task/PR/history that will rot:
// added in M3 to fix the bug where messages didn't sync
```

**Language: English only.** Even though our team conversation is in Chinese, all code, identifiers, comments, commit messages, log lines, and error strings must be English. Reason: portability, tooling, and so the codebase stays readable by anyone who joins later.

### 1.4 File Layout Inside a Package

For any non-trivial package:

```
internal/<pkg>/
├── doc.go             # package-level doc comment
├── <pkg>.go           # main types and constructor
├── <pkg>_test.go      # tests
├── <subfeature>.go    # split when one file > ~400 lines
└── internal/          # package-private subpackages (rare)
```

### 1.5 Logging

- Use `log/slog` everywhere.
- One logger per long-lived component, passed in via constructor.
- Levels:
  - `Debug`: dev-time tracing, off in prod
  - `Info`: lifecycle events (daemon start, account online/offline)
  - `Warn`: recoverable anomalies
  - `Error`: things that broke a user-facing operation
- Always include structured fields, not formatted strings:
  ```go
  // Good
  log.Info("account online", "account_id", id, "discord_bot", botName)
  // Bad
  log.Info(fmt.Sprintf("account %s online (bot=%s)", id, botName))
  ```

### 1.6 Error Handling

- Define error codes in `internal/errcode/` from M2 onwards.
- Every API handler converts internal errors into a structured response:
  ```json
  { "error": { "code": "AUTH_INVALID", "message": "...", "details": { } } }
  ```
- CLI maps error codes to exit codes via a single table (`internal/errcode/exitcode.go`).
- **Never `panic` in production code paths.** `panic` is reserved for "programmer error" (nil pointer that the type system should have caught). Recover at goroutine boundaries.

### 1.7 Concurrency

- **Prefer channels for ownership transfer, mutexes for protecting shared state.**
- Every public method that touches shared state must be safe for concurrent callers — document it in the godoc.
- Long-running goroutines use `context.Context` for cancellation, never a `chan struct{}` `done`.
- No `time.Sleep` in production code paths — use `time.After(ctx)` / `time.NewTimer(...)` with proper cleanup.

### 1.8 Dependency Discipline (from §11.1 of architecture)

Before writing the first line that imports a third-party package:

1. WebFetch the official docs (GitHub README + godoc).
2. Confirm the major version we're targeting (e.g. `github.com/go-chi/chi/v5`, not `v4`).
3. Confirm any key API signatures we're about to call.
4. Add a one-line comment near the import recording the version + doc URL when the choice was non-obvious.

This applies to **every** third-party package, including "obvious" ones like cobra and chi.

**Audit trail requirement (from M1-P3-002):** when adding or upgrading a third-party
package, the milestone's Phase 1 report MUST record, for each new/upgraded dep:

- exact official doc URL(s) consulted
- version (and minor / patch) checked
- date the docs were checked

A future audit must be able to replay the dependency decision without guessing.

---

## Part 2 — Per-Milestone Three-Phase Workflow

Every milestone (M1 through M8) goes through three phases. **A milestone is not "done" until all three phases pass.**

**Phase 1 and Phase 2 are executed as a single continuous block by the developer.** The developer writes code and tests interleaved (TDD-friendly), without pausing between them. **The only hard pause is between Phase 2 and Phase 3** — that is where control hands off to a fresh auditor agent.

```
┌─────────────────────────────────────────────────┐    ┌──────────────────┐
│  Phase 1 + Phase 2 (continuous block)            │    │  Phase 3         │
│                                                  │    │  External Audit  │
│  Implementation interleaved with Testing.        │    │  (Fresh reviewer)│
│  Developer writes code and tests together,       │    │                  │
│  with QA mindset throughout.                     │ -> │  Spawned via     │
│                                                  │    │  Agent subagent  │
│  Two reports still produced (separate concerns): │    │  with no prior   │
│   - docs/milestones/M<N>-phase1.md (what built)  │    │  context.        │
│   - docs/milestones/M<N>-phase2.md (how tested)  │    │                  │
└─────────────────────────────────────────────────┘    └──────────────────┘
        │                                                        │
        ▼                                                        ▼
   working code + tests + 2 reports                       audit report
   (no pause, no user signoff yet)                              │
                                                  ┌─────────────┴─────────────┐
                                                  ▼                           ▼
                                           issues resolved             issues triaged
                                           (loop back into             (logged for later
                                            Phase 1+2 block)            if non-blocking)
```

**Why the merge:** separating code and tests into sequential phases tempts the developer to "finish coding then test", which produces brittle code and shallow tests. Doing them together keeps the QA mindset alive while writing implementation. The two **reports** stay separate because they answer different questions ("what did I build?" vs "how did I prove it?"), but the **work** is one block.

### Phase 1 — Implementation

**Mindset:** developer. Write the code that delivers the milestone's scope.

**Inputs:**
- Milestone scope from `04-roadmap.md`
- Coding rules from Part 1 of this document
- Architecture decisions from `03-architecture.md`

**Activities:**
1. Read the relevant milestone section in `04-roadmap.md` end-to-end.
2. Confirm all required third-party libraries' docs (Rule 1.8).
3. Implement the scope.
4. Make sure the code **compiles and runs**: `make build` succeeds, the daemon starts, the CLI's relevant commands return correct output for the happy path.
5. Update user-facing docs (README, CLI help text) for any new commands.
6. Run `go fmt`, `go vet`, `staticcheck` (if available); fix everything they report.

**Phase 1 exit criteria:**
- All scope items in the milestone are implemented.
- Code passes `go build ./...`, `go fmt`, `go vet`.
- Happy-path demo from the milestone runs successfully by hand.
- All new public symbols have godoc comments (English).
- A short "Phase 1 report" is written into `docs/milestones/M<N>-phase1.md`:
  - Files added / modified
  - Decisions made during implementation that weren't in the design docs
  - Known gaps / TODOs (deliberately deferred)
  - Demo command(s) that reproduce the milestone goal

### Phase 2 — Testing

**Mindset:** QA engineer. Adversarial, edge-case-driven, "how do I break this?" Not "how do I prove this works."

**Inputs:**
- Phase 1 code
- Phase 1 report (to know what was deferred, so we don't waste effort testing it)

**Activities — write tests across all relevant levels:**

| Level | When to write | Tool |
|---|---|---|
| **Unit tests** | Every business package with non-trivial logic | std `testing` + `testify/assert` and `testify/require` |
| **Repository tests** | Every store interface implementation | `:memory:` SQLite with real migrations |
| **HTTP API tests** | Every handler in `internal/api/` | `httptest.Server` + `pkg/client` |
| **State engine timing tests** | M5 and later | manual time injection (`clock` interface) |
| **End-to-end demo scripts** | Every milestone with new user-visible commands | bash scripts under `e2e/` using mock `bot.Provider` |
| **Manual Discord verification** | M3, M4, M6, M7 | hand-run checklist; results recorded |

**Test discipline:**
- **Table-driven** wherever there are >2 cases.
- Cover at least: happy path / boundary / invalid input / unauthorized / not found / concurrent access (where relevant).
- Each milestone aims for **≥ 70% line coverage** on **code-bearing packages** (those with real implementation, not just `doc.go` placeholders). Project-total coverage is not a milestone gate — placeholder packages dilute it without signal. Phase 2 reports must list per-code-bearing-package coverage and explicitly note placeholder packages as N/A.
- **No flaky tests.** A test that fails sometimes is worse than no test.
- **No tests against Discord production** — those are M3 manual verifications, not automated tests.

**Phase 2 exit criteria:**
- `go test ./... -race` passes with no failures and no race warnings.
- Coverage report generated; record per-package coverage in the Phase 2 report.
- E2E demo scripts in `e2e/` run green.
- A "Phase 2 report" is written into `docs/milestones/M<N>-phase2.md`:
  - Test files added
  - Coverage numbers per package
  - Edge cases / failure modes exercised
  - Bugs found and fixed during testing (with brief description)
  - Known weaknesses tests *cannot* cover (e.g. real Discord behavior)

### Phase 3 — External Audit

**Mindset:** a senior engineer who has never seen this code, has the requirements + architecture docs in hand, and is paid to find problems.

**Crucial:** the auditor is **not the developer**. In our setup, the auditor is a **fresh `Agent` subagent** with no conversation history — it sees only the documents and the code, not the developer's intent.

**Activities:**

1. **Spawn a fresh audit agent** via the `Agent` tool with `subagent_type: general-purpose`. Brief it with:
   - The requirements baseline: `docs/02-requirements-final.md`
   - The architecture: `docs/03-architecture.md`
   - The coding rules: `docs/05-engineering-workflow.md` (this doc)
   - The milestone scope: relevant section of `docs/04-roadmap.md`
   - The Phase 1 and Phase 2 reports
   - The code changes (a list of files; the agent reads them itself)

2. **Ask the auditor to produce an issue list** along these dimensions:
   - **Correctness:** does the code actually implement what the milestone scope says?
   - **Decoupling:** any forbidden imports? any business code that knows about Discord?
   - **Clarity:** any function that's hard to follow? any naming that misleads?
   - **Comments:** are they in English, accurate, non-obvious? are public symbols documented?
   - **Concurrency:** any race risks, goroutine leaks, missing context propagation?
   - **Error handling:** swallowed errors, missing context, panic in production paths?
   - **Test coverage gaps:** important behaviors that have no test?
   - **Architecture drift:** any decision that contradicts `03-architecture.md` without justification?
   - **Security:** token handling, file permissions, SQL injection, command injection.

3. **The audit report goes into `docs/milestones/M<N>-phase3.md`** with each issue:
   - Severity (Blocker / Major / Minor / Nit)
   - File:line reference
   - Description
   - Suggested fix (the auditor proposes; the developer decides)

4. **Triage with the user:**
   - **Blocker / Major** issues: must be fixed before the milestone is closed → loop back to Phase 1.
   - **Minor / Nit** issues: user decides — fix now, defer to a tracked TODO, or wontfix.

**Phase 3 exit criteria:**
- Audit report exists.
- All Blocker and Major issues are resolved (or formally accepted by the user with a written reason).
- Triage decisions are recorded in the audit report's footer.

**Why an Agent subagent and not just self-review:**
- A fresh agent has no developer bias ("I know what I meant").
- It reads the code against the *documents*, not against the developer's mental model.
- It's cheap and repeatable — every milestone, same protocol.

**Optional escalation:** for milestones that touch sensitive areas (M2 auth, M3 Discord adapter, M7 attachment handling), the user may additionally run `/ultrareview` (cloud multi-agent review) or `/security-review`. These are user-triggered.

---

## Part 3 — Milestone Closeout

When all three phases of a milestone are complete:

1. **Commit boundary:** the milestone's final state should be a clean point in git history. Tag commit as `m<N>-complete` (lightweight tag is fine).
2. **Update `docs/00-overview.md`** with a one-line note: "M<N> complete, <date>".
3. **Stop and wait for user signoff** before starting the next milestone. The user reviews the three reports and says "go" before M<N+1> begins.

---

## Part 4 — Per-Milestone Document Set

After each milestone, the following files exist under `docs/milestones/`:

```
docs/milestones/
├── M1-phase1.md       # implementation report
├── M1-phase2.md       # testing report
├── M1-phase3.md       # audit report + triage decisions
├── M2-phase1.md
├── M2-phase2.md
├── M2-phase3.md
└── …
```

These files are the durable record of how the milestone was built, tested, and reviewed. They are not throwaway — they're consulted when investigating regressions or planning future work.

---

## Part 5 — Quick Checklist (printable)

```
PHASE 1 + 2 — IMPLEMENT & TEST (continuous, no pause)
[ ] read milestone scope in 04-roadmap.md
[ ] WebFetch docs of every new third-party lib
[ ] implement scope, interleaving tests as code lands
[ ] unit tests for every non-trivial biz package
[ ] repo tests against :memory: SQLite
[ ] HTTP API tests via httptest
[ ] e2e script in e2e/ runs green
[ ] go fmt / go vet / staticcheck clean
[ ] go test ./... -race clean
[ ] coverage ≥ 70% on new biz code (or explained)
[ ] happy-path demo runs
[ ] godoc on all new exported symbols
[ ] write docs/milestones/M<N>-phase1.md (what built)
[ ] write docs/milestones/M<N>-phase2.md (how tested)

═══ HARD PAUSE: hand off to fresh auditor ═══

PHASE 3 — AUDIT
[ ] spawn fresh audit agent (subagent_type: general-purpose)
[ ] brief with reqs + arch + rules + milestone scope + phase 1/2 reports
[ ] receive issue list
[ ] write docs/milestones/M<N>-phase3.md
[ ] triage with user
[ ] all Blocker/Major fixed (or formally accepted)
[ ] tag commit m<N>-complete
[ ] WAIT for user "go" before next milestone
```

---

## Part 6 — Anti-Patterns (red flags to refuse)

If you catch yourself or the auditor catches you doing any of these, **stop and rework**:

- "I'll write tests later" — Phase 2 is not optional.
- "It's just a small fix, skip the audit" — Phase 3 is not optional.
- "The mock is too complex, let me just use the real Discord client in this test" — no, fix the mock.
- "I'll fix the Major issue in the next milestone" — no, fix it now or get the user to formally accept it.
- "This package directly imports discordgo because it's easier" — no, go through `bot.Provider`.
- "Let me add a TODO and move on" — TODOs are tracked debt; if it's Blocker/Major, it doesn't get a TODO, it gets fixed.
- "I'll write the comment in Chinese, it's faster" — no, English only.
- "I know cobra's API, no need to check docs" — Rule 1.8 has no exceptions.
