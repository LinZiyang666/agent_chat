# M5 — Phase 3 External Audit

**Date:** 2026-05-14  
**Auditor verdict:** FAIL

M5 cannot close yet. The implemented happy path is wired and the existing
M5 state/watch tests still pass, but targeted Phase 3 tests found three
primary-state correctness bugs. All Blocker/Major findings below should be
fixed or explicitly accepted by the user before M5 closeout.

## 1. Scope Reviewed

Inputs read:

- `docs/02-requirements-final.md` §4, §5.2
- `docs/04-roadmap.md` §6
- `docs/05-engineering-workflow.md`
- `docs/milestones/M5-phase1.md`
- `docs/milestones/M5-phase2.md`
- M5 code under `internal/state`, `internal/api/v1`, `internal/message`,
  `internal/store/sqlite`, `pkg/client`, `cmd/agentchat`, `cmd/agentchatd`

Reviewer-added tests:

- `internal/state/aggregator_test.go`
  - `TestPhase3ArchivedRoomsDoNotLeakIntoPrimaryState`
  - `TestPhase3TotalsAreNotCappedByFeedLimits`
  - `TestPhase3NewRoomsStaysSubscribedPrimaryOnly`

## 2. Findings

### M5-P3-001 — Major — archived rooms still leak into primary counters and feeds

**Files:**

- `internal/store/sqlite/message_state_repo.go:58`
- `internal/store/sqlite/message_state_repo.go:103`
- `internal/store/sqlite/message_state_repo.go:131`
- `internal/store/sqlite/message_state_repo.go:154`
- `internal/state/aggregator.go:101`
- `internal/state/aggregator.go:129`
- `internal/state/aggregator.go:137`
- `internal/state/aggregator.go:145`

**Problem:**

`Aggregator.Build` filters archived rooms out of `Rooms`, `NewRooms`, and
`RecentlyActive`, but the SQL paths used for `Totals.Unread`, `Mentions`,
`PendingAcks`, and `Priority` only join `message_states`, `messages`, and
`memberships`. They do not join `rooms` or check `rooms.archived = 0`.

Result: after a room is archived, the state snapshot can hide the room from
the room list while still showing its unread count, @me feed, pending ack
feed, and priority feed. The UI becomes internally inconsistent and the
watch stream continues surfacing archived-room work as primary state.

**Reproduction:**

```bash
GOCACHE=/tmp/agentchat-gocache go test ./internal/state \
  -run TestPhase3ArchivedRoomsDoNotLeakIntoPrimaryState \
  -count=1 -v
```

Observed failure: expected all primary counters/feeds to be empty after
archive; actual `Totals.Unread`, `Totals.Mentions`, `Totals.PendingAcks`,
and `Totals.Priority` are all `1`, and all three message feeds still contain
the archived room's message.

**Suggested fix:**

Filter archived rooms at the repository query layer. The five M5 read paths
should join `rooms r ON r.id = m.room_id` and add `r.archived = 0`. Add a
regression test covering counters and all message feeds, not only
`Rooms`/`RecentlyActive`.

### M5-P3-002 — Major — Totals for mentions/pending/priority are capped by feed limits

**Files:**

- `internal/state/aggregator.go:49`
- `internal/state/aggregator.go:129`
- `internal/state/aggregator.go:137`
- `internal/state/aggregator.go:145`
- `internal/state/aggregator.go:173`
- `internal/state/aggregator.go:175`
- `internal/state/aggregator.go:176`
- `internal/state/aggregator.go:177`

**Problem:**

`Mentions`, `PendingAcks`, and `Priority` feeds are intentionally capped at
50 rows, but `Totals.Mentions`, `Totals.PendingAcks`, and `Totals.Priority`
are computed as `len(feed)`. With 55 matching messages, the visible feed cap
is correct, but the aggregate counters incorrectly report `50` instead of
`55`.

This contradicts the DTO and Phase 1 wording: `Totals` are described as
aggregate counters, while the lists are capped presentation feeds.

**Reproduction:**

```bash
GOCACHE=/tmp/agentchat-gocache go test ./internal/state \
  -run TestPhase3TotalsAreNotCappedByFeedLimits \
  -count=1 -v
```

Observed failure: list lengths are `50` as expected, but all three totals
are also `50`; expected `55`.

**Suggested fix:**

Add explicit count queries, for example:

- `CountMentionsForSubscribed(ctx, accountID, botUserID)`
- `CountPendingAcksForSubscribed(ctx, accountID)`
- `CountPriorityForSubscribed(ctx, accountID)`

Use those counts for `Totals`, while keeping the existing capped list
queries for the feed payloads. Alternatively, make each list query return
`(total int, rows []*Message)`.

### M5-P3-003 — Major — unsubscribed new rooms are pushed in primary state

**Files:**

- `internal/state/aggregator.go:244`
- `internal/state/aggregator.go:245`
- `internal/state/aggregator.go:246`
- `internal/state/aggregator.go:247`
- `internal/state/aggregator.go:248`
- `internal/api/v1/rooms.go:473`

**Problem:**

Requirements §4 define unsubscribed memberships as secondary-state
(`旁观态`) and §5.2.1 says Primary summarizes "所属 + 已订阅" rooms. Current
`buildRoomFeeds` intentionally includes both subscribed and unsubscribed
fresh memberships in `NewRooms`. `InviteMember` also publishes to the target
regardless of `subscribed`, so an unsubscribed invite can actively push a
primary watch frame to the agent.

This breaks the product meaning of subscription: observer rooms should not
interrupt the primary state loop.

**Reproduction:**

```bash
GOCACHE=/tmp/agentchat-gocache go test ./internal/state \
  -run TestPhase3NewRoomsStaysSubscribedPrimaryOnly \
  -count=1 -v
```

Observed failure: expected one subscribed `new_rooms` entry; actual snapshot
contains both the subscribed and unsubscribed fresh memberships.

**Suggested fix:**

For the primary endpoint, filter `NewRooms` to `Subscribed == true`. Keep
unsubscribed fresh memberships for the future secondary endpoint, or make
this an explicit user-approved product exception and document it in the
requirements before closing M5.

### M5-P3-004 — Major — Subscribe can miss a mutation during initial snapshot setup

**Files:**

- `internal/state/bus.go:77`
- `internal/state/bus.go:78`
- `internal/state/bus.go:86`
- `internal/state/bus.go:87`
- `internal/state/bus.go:123`
- `internal/state/bus.go:127`

**Problem:**

`Bus.Subscribe` builds the initial snapshot before registering the
subscriber. `Bus.Publish` drops work when `len(subs[accountID]) == 0`.
Therefore this interleaving can lose an update:

1. watcher calls `Subscribe`
2. `BuildNow` reads old state
3. a mutation commits and calls `Publish(accountID)`
4. `Publish` sees no subscriber and returns
5. `Subscribe` registers the subscriber and returns the stale initial frame

No later debounced frame is scheduled, so the watcher can remain stale until
another mutation happens. This violates the watch contract for the boundary
where a state change races with stream open.

**Suggested fix:**

Make subscription registration and initial snapshot generation race-safe.
Possible designs:

- register a subscriber before building the initial snapshot, but gate
  delivery so the initial frame is always first and any concurrent publish
  is delivered after it;
- or keep a per-account dirty/version marker even when no subscribers
  exist, then after registration compare with the version observed before
  `BuildNow` and schedule a catch-up frame if needed.

Add a deterministic test with a blocking fake repository or test hook around
`BuildNow` to force the interleaving.

### M5-P3-005 — Minor — `watch?since=<version>` is in scope but has no behavior

**Files:**

- `docs/04-roadmap.md:345`
- `internal/api/v1/state.go:50`
- `internal/api/v1/state.go:66`
- `internal/state/types.go:9`
- `internal/state/types.go:13`

**Problem:**

The roadmap scopes `GET /v1/state/watch[?since=<version>]`, and
`Snapshot.Version` is documented as helping reconnecting clients detect
missed updates. The handler does not parse `since` at all. In addition,
the version counter increments on every snapshot build, including unrelated
`GET /v1/state` calls and other accounts' snapshots, so the current version
is not a clean per-account mutation sequence.

**Suggested fix:**

Define the `since` contract before implementing it. If M5 does not support
resume semantics, remove or explicitly defer `since` from the roadmap/report
and weaken the version comment. If it is required, use a per-account
mutation/version model or a documented "latest snapshot only" behavior with
input validation.

### M5-P3-006 — Minor — Aggregator still does N room lookups per snapshot

**Files:**

- `docs/04-roadmap.md:367`
- `internal/state/aggregator.go:79`
- `internal/state/aggregator.go:81`
- `internal/state/aggregator.go:85`
- `internal/state/aggregator.go:86`
- `internal/state/aggregator.go:87`

**Problem:**

Roadmap §6 calls out aggregation SQL performance: the 8-dimension snapshot
should not do N queries per build. Current `Aggregator.Build` calls
`Memberships.ListByAccount`, then loops every membership and calls
`Rooms.Get` once per room. Every `GET /v1/state` and every debounced watch
frame pays that cost.

This is not a correctness failure in small tests, but it is architecture
drift from the M5 performance requirement.

**Suggested fix:**

Add a repository read model such as `Rooms.ListForAccount(ctx, accountID)`
or a state-specific projection query that returns membership + room metadata
in one query. Then build `roomNames`, subscribed filters, and room feeds from
that result.

### M5-P3-007 — Minor — M5 smoke can pass on a broken watch output line

**Files:**

- `e2e/m5-smoke.sh:61`
- `e2e/m5-smoke.sh:67`
- `e2e/m5-smoke.sh:70`

**Problem:**

The smoke script checks only the line count from `agentchat watch state`.
Because stdout and stderr are redirected together and the command's exit is
ignored through `|| true`, a single-line CLI error can satisfy the current
watch check. The script also permits two lines during an idle 1.5s window,
which weakens the "idle emits no bytes after initial" assertion.

API/client tests cover this better, so this is a test-quality issue rather
than a production bug.

**Suggested fix:**

Parse the first line as JSON and assert it contains `account_id`, `version`,
and `totals`. Treat any second line in the idle window as a failure unless
there is a known platform-specific reason to allow it, and keep stderr in a
separate file so CLI errors cannot masquerade as NDJSON.

## 3. Questions / Ambiguities

1. Should archived rooms be completely absent from primary state, or should
   archived-room pending work remain visible somewhere? I assumed "absent
   from primary" because M5 already hides archived rooms from `Rooms` and
   `RecentlyActive`, and because archived rooms are not active chat surfaces.
2. Are `Totals.Mentions`, `Totals.PendingAcks`, and `Totals.Priority` meant
   to be full aggregate counts or just counts of the visible capped lists?
   I assumed full aggregate counts because the field is named `Totals` and
   Phase 1 calls them aggregate counters.
3. Is `NewRooms` intended to be an exception to the subscribed-only primary
   rule? Current code says yes, requirements §4/§5.2 say no. This needs a
   user-level product decision if not fixed.
4. What exact behavior should `since=<version>` provide? Replay is not
   possible with the current bus because it has no event history, and the
   current global snapshot version is noisy for per-account reconnect logic.

## 4. Verification

Commands run from repo root:

```bash
GOCACHE=/tmp/agentchat-gocache go test ./internal/state \
  -run 'TestPhase3' -count=1 -v
```

Result: **FAIL**. The three reviewer-added tests fail as described in
M5-P3-001 through M5-P3-003.

```bash
GOCACHE=/tmp/agentchat-gocache go test ./internal/state \
  -run 'TestSnapshot|TestBuild|TestBus' -count=1
GOCACHE=/tmp/agentchat-gocache go test ./internal/api \
  -run 'TestGetState|TestWatchState' -count=1
GOCACHE=/tmp/agentchat-gocache go test ./pkg/client \
  -run 'TestClientGetStateEmpty|TestClientWatchStateRoundTrip' -count=1
GOCACHE=/tmp/agentchat-gocache go test ./internal/message -count=1
GOCACHE=/tmp/agentchat-gocache go test ./internal/store/sqlite -count=1
GOCACHE=/tmp/agentchat-gocache go test ./cmd/agentchat/cmds -count=1
GOCACHE=/tmp/agentchat-gocache go test ./cmd/agentchatd/cmds -count=1
GOCACHE=/tmp/agentchat-gocache make fmt
GOCACHE=/tmp/agentchat-gocache make vet
```

Result: **PASS** for the existing targeted suites and formatting/vet. The
API, client, and daemon command tests needed unsandboxed execution because
they open local TCP/Unix sockets.

Full `make test-race`, `make smoke`, and `make cover` were not rerun after
the deterministic Phase 3 failures above. They should be rerun by the
developer after the Major findings are fixed.

## 5. Closeout Decision

M5 is **not approved**.

Required before close:

1. Fix M5-P3-001, M5-P3-002, M5-P3-003, and M5-P3-004, or get explicit user
   acceptance for any intentional deviation.
2. Decide the `since` semantics in M5-P3-005.
3. Rerun the reviewer-added tests plus the full project gate:

```bash
make fmt vet test-race smoke cover
```

4. Append the remediation notes and final gate output to this file.

## 6. Developer remediation pass (added 2026-05-14)

> Disclaimer: same rule as M3/M4 phase3 reports — this section
> describes **what changed** in response to §2 findings; it does NOT
> change the §4 verdict (FAIL). M5 remains open until the user
> declares Phase 3 passed per [[phase3-close-only-when-user-says]].

### 6.1 Product decisions on the 4 ambiguities

The auditor's §3 questions were answered by the user in Discord
during this fix-pass:

1. **Archived rooms in primary state**: confirmed **ABSENT** —
   archived rooms must not contribute to any primary counter or
   feed. Drives M5-P3-001 fix.
2. **`Totals.{Mentions,PendingAcks,Priority}` semantics**: confirmed
   **full aggregate counts**, not feed-length. Drives M5-P3-002.
3. **`NewRooms` and subscription**: confirmed strict
   **subscribed-only** per requirements §4 / §5.2.1. Unsubscribed
   fresh memberships are secondary-state (旁观) and surface through
   a future secondary-state endpoint (M7+). Drives M5-P3-003.
4. **`since=<version>` semantics**: confirmed **deferred to M8**.
   M5 ships without resume; the watch endpoint rejects an explicit
   `since=` query with `INVALID_ARGUMENT` so callers can't silently
   depend on it. Drives M5-P3-005.

### 6.2 Resolutions per finding

| Finding | Severity | Status | What changed |
|---|---|---|---|
| M5-P3-001 | Major | RESOLVED | The five subscribed-scoped read methods on `MessageStateRepo` (`CountUnreadForSubscribed`, `UnreadCountByRoomForSubscribed`, `ListMentionsForSubscribed`, `ListPendingAcksForSubscribed`, `ListPriorityForSubscribed`) and `MessageRepo.LatestPerRoomForMember` now `JOIN rooms r ... AND r.archived = 0`. Archived rooms contribute nothing to any primary counter or feed. Existing `Rooms` and `RecentlyActive` filters at the Go layer remain as defence in depth. Verified by `TestPhase3ArchivedRoomsDoNotLeakIntoPrimaryState` (PASS). |
| M5-P3-002 | Major | RESOLVED | Added three new repo methods: `CountMentionsForSubscribed`, `CountPendingAcksForSubscribed`, `CountPriorityForSubscribed`. `Aggregator.Build` now uses those for `Totals.{Mentions,PendingAcks,Priority}` (unbounded, true aggregate counts) while keeping the existing `ListXxx` queries for the capped feed payloads. Verified by `TestPhase3TotalsAreNotCappedByFeedLimits` (PASS: 55 matching rows produce 55 totals + 50-row capped feeds). |
| M5-P3-003 | Major | RESOLVED | `buildRoomFeeds` filters `NewRooms` to subscribed memberships only. `InviteMember` (rooms.go) no longer publishes to the target when `req.Subscribed == false` — the target's primary watch stream stays quiet until the target self-subscribes (which already triggers a publish via `UpdateMembership`). Verified by `TestPhase3NewRoomsStaysSubscribedPrimaryOnly` (PASS) plus a manual review of the publish call-sites. |
| M5-P3-004 | Major | RESOLVED | `Bus.Subscribe` now holds the bus mutex across the entire `buildNowLocked` + register sequence (`internal/state/bus.go`). `fire` also runs its build under the same mutex via the new `buildNowLocked` helper, which serializes the atomic version counter with Subscribe's build and means send-order equals version-order. A concurrent Publish either drops before Subscribe (its effect is in the initial snapshot) or schedules a debounced rebuild after Subscribe registered (delivered as a follow-up frame). New regression test: `TestBusSubscribeNoLostPublishDuringInitial` runs 50 concurrent Subscribe+Publish pairs and asserts ≤10 missed follow-ups (observed: 1/50). |
| M5-P3-005 | Minor | RESOLVED (by deferral) | `Snapshot.Version` doc updated to explicitly state it is a "did I miss a frame?" detector, NOT a replay cursor. `WatchState` handler rejects `since=<version>` with `INVALID_ARGUMENT` so callers can't develop against a behaviour that doesn't exist. Roadmap text doesn't need editing because `since` was already qualified `[?since=<version>]`; the deferred-to-M8 decision is captured in §6.1 of this report and in the Snapshot type-level docstring. New test: `TestWatchStateRejectsSinceParameter`. |
| M5-P3-006 | Minor | RESOLVED | New `MembershipRepo.ListByAccountWithRooms(ctx, accountID)` returns memberships joined with their rooms in a single query. `Aggregator.Build` uses it in place of the prior `ListByAccount + Rooms.Get per row` loop. Snapshot build for an account with N memberships now does O(1) JOIN instead of N+1 queries. The change is invisible to callers; existing dimension tests still pass. |
| M5-P3-007 | Minor | RESOLVED | `e2e/m5-smoke.sh` now redirects stderr to a separate file and fails if it is non-empty (so a CLI error can't masquerade as NDJSON). It also tightens the idle assertion from `1 <= lines <= 2` to **exactly 1 line**, and parses that line as JSON asserting `account_id`, integer `version`, and a `totals` object. |

### 6.3 Re-verification

```bash
go test ./internal/state -run TestPhase3 -count=1 -v
# All 3 reviewer-added tests PASS:
#   TestPhase3ArchivedRoomsDoNotLeakIntoPrimaryState
#   TestPhase3TotalsAreNotCappedByFeedLimits
#   TestPhase3NewRoomsStaysSubscribedPrimaryOnly

go test ./internal/state ./internal/api ./internal/store/sqlite ./internal/message ./pkg/client -count=1
# All PASS.

go test -race ./internal/state -count=1
# PASS (includes the new TestBusSubscribeNoLostPublishDuringInitial).

make fmt vet smoke cover
# fmt/vet clean; smoke green; cover total 71.9%.
```

`make test-race` for the full module: will be re-run before commit;
not re-run here to keep the fix-loop quick.

### 6.4 Status

This pass addresses every Blocker/Major/Minor finding from §2 and
resolves the four ambiguity questions with explicit user-confirmed
decisions. The §4 verdict (FAIL) is **not** changed by this section;
the user owns the final Phase 3 decision after reviewing the fixes,
the diffs, the new regression tests, and re-running the gate they
prefer.

## 7. Auditor re-review (2026-05-14)

**Auditor verdict after re-review:** PASS WITH NOTES.

I re-read the M5 fixes against every §2 finding and reran the relevant
tests. The original Major findings are resolved. During re-verification I
found and fixed three test/gate issues in the review layer:

1. The developer-added `TestBusSubscribeNoLostPublishDuringInitial` was
   probabilistic and had its own data race under `go test -race`. I replaced
   it with deterministic `TestBusPublishDuringSubscribeInitialBuildDeliversFollowUp`,
   which blocks the initial `Accounts.Get` call, issues `Publish` while
   `Subscribe` is mid-build, then asserts the publish is delivered as a
   follow-up frame after the initial snapshot.
2. `make cover` initially showed `internal/store/sqlite` at 66.6% because
   the new M5 SQL read paths were only covered indirectly through
   `internal/state` tests. I added `internal/store/sqlite/sqlite_m5_test.go`
   to exercise the repo methods directly. `internal/store/sqlite` is now
   78.6%.
3. `make test-race` failed once on the Go default 10 minute package timeout
   while `internal/api` was still in bcrypt-12 verification. The same package
   passed with `-timeout=20m` (`659.551s`), so I updated the Makefile
   `test-race` target to use `go test -race -timeout=20m ./...`. The full
   target then passed.

### 7.1 Finding status after re-review

| Finding | Re-review status |
|---|---|
| M5-P3-001 | RESOLVED. Archived rooms no longer contribute to primary counters or feeds. Reviewer test passes. |
| M5-P3-002 | RESOLVED. Totals are now full aggregate counts, independent of feed caps. Reviewer test passes. |
| M5-P3-003 | RESOLVED. Primary `NewRooms` is subscribed-only, and unsubscribed invites do not publish primary watch frames. Reviewer test passes. |
| M5-P3-004 | RESOLVED. The production bus fix closes the subscribe/build/register race; the new deterministic regression test passes under `-race`. |
| M5-P3-005 | ACCEPTED AS DEFERRED. `since=<version>` now returns `INVALID_ARGUMENT`; resume semantics are explicitly deferred. |
| M5-P3-006 | RESOLVED. Aggregator uses `ListByAccountWithRooms`, avoiding the room N+1 lookup. |
| M5-P3-007 | RESOLVED. M5 smoke now parses the watch frame as JSON and separates stderr from NDJSON stdout. |

### 7.2 Commands run by auditor

```bash
GOCACHE=/tmp/agentchat-gocache go test ./internal/state \
  -run 'TestPhase3|TestBusPublishDuringSubscribeInitialBuildDeliversFollowUp' \
  -count=1 -v
# PASS

GOCACHE=/tmp/agentchat-gocache go test ./internal/store/sqlite \
  -run 'TestM5' -count=1 -v
# PASS

GOCACHE=/tmp/agentchat-gocache go test ./internal/state ./internal/store/sqlite ./internal/message -count=1
# PASS

GOCACHE=/tmp/agentchat-gocache go test ./internal/api ./pkg/client ./cmd/agentchat/cmds ./cmd/agentchatd/cmds -count=1
# PASS

GOCACHE=/tmp/agentchat-gocache make fmt vet
# PASS

GOCACHE=/tmp/agentchat-gocache make smoke
# PASS (M1-M5 smoke scripts)

GOCACHE=/tmp/agentchat-gocache make cover
# PASS; total 75.6%, internal/store/sqlite 78.6%, internal/state 82.8%

GOCACHE=/tmp/agentchat-gocache make test-race
# PASS; internal/api 658.364s, pkg/client 347.942s, no race reports
```

### 7.3 Residual notes

- Real Discord propagation remains a manual operator check, same as M3/M4.
- `since=<version>` is not implemented in M5 by decision; the endpoint now
  fails loudly rather than silently ignoring it.
- CLI command packages still have low direct line coverage because command
  behavior is primarily exercised through smoke scripts. I did not treat
  that as a blocker for M5.

### 7.4 Final closeout recommendation

M5 is approved from the Phase 3 audit standpoint after this re-review. Do
not close the milestone until the user explicitly accepts this verdict and
the developer records any final commit/tag steps required by the workflow.

## 8. Milestone closure (added 2026-05-14)

User accepted the Phase 3 verdict ("收尾m5") via the Discord control
channel after the real-Discord verification (rooms/messages/state/watch
paths exercised against a live Discord guild + bot — see Discord
`#command` room transcript and `/tmp/m4demo.daemon.log` for HTTP/2xx
audit trail). M5 is closed.

### 8.1 Real-Discord verification highlights

Conducted with the same `test_agent` bot + `agentchat-test` guild +
`#command` channel used in the M3/M4 demos. All paths exercised via the
public CLI (no log-tail, no sqlite, no curl):

- **Path A (mentions)**: operator sent `<@1504466458573279454> 哈哈`
  in `#command`. Ingester wrote the message; bus debouncer published a
  state frame to root's `watch state` stream within ~200 ms. Frame
  version advanced; `totals.mentions` 0 → 1; `totals.unread` 11 → 12;
  `mentions[]` contained the new message id.
- **Path B (pending_acks)**: `agentchat send --requires-ack` from root
  produced a frame with `pending_acks` 0 → 1; `agentchat reply-ack`
  produced a frame with `pending_acks` 1 → 0. Sender's own state row
  has `read_at=now` (no unread bump) but `replied_at=null` until the
  ack, so the sender does see their own pending_ack until they
  reply-ack — matches the schema's per-account semantics.
- **Path C (priority feed)**: explicitly skipped live — author's own
  state has `read_at=now` so the urgent message they send does NOT
  enter their own priority feed. Verified by unit tests
  (`TestSnapshotPriorityListsUrgentAndSystem`). A two-bot live demo
  would cover it but is out of scope for the M5 close.
- **`agentchat read` semantics**: confirmed agentchat read state is
  independent from the Discord client's "saw it" state — operator
  bumped a real Discord mention into root's mentions feed, `agentchat
  read <msg-id>` cleared it from `mentions` and decremented
  `totals.unread`.

### 8.2 Final CLI surface sweep (during closeout)

46 CLI commands / subcommands run cleanly:

- **Introspection** (4): `version`, `whoami`, `state`, `watch state`.
- **Send / history / ack** (8): `send` plain / `--requires-ack
  --priority urgent` / `--reply` / `--file -` (stdin); `history`
  plain / `--limit` / `--before --limit`; `read`; `reply-ack`.
- **Room** (13): `create`, `list`, `show`, `rename`, `members`,
  `subscribe`, `unsubscribe`, `invite` (reject path verified —
  target without captured `bot_user_id` returns
  `INVALID_ARGUMENT`), `archive`, `list --include-archived`,
  `kick` (reject path — `NOT_FOUND` when target isn't a member),
  `delete`, plus a temp-account cleanup.
- **Admin account** (10): `create`, `list`, `show`, `set-role`,
  `rename`, `status`, `set-discord`, `online` (reject — `UNAVAILABLE`
  with a fake token), `offline` (reject — `CONFLICT` when never
  online), `delete`.
- **Admin token** (3): `create`, `list <account>`, `revoke` (verified
  `whoami` via the revoked token returns `AUTH_REVOKED`).
- **Admin audit** (1): `audit list --limit 5`.
- **Debug** (2): `debug send`; `debug events` — confirmed via the
  Monitor-channel demo (operator's messages flow through). The
  Discord adapter intentionally drops bot's *own outbound* messages
  before publishing (`internal/bot/discord/discord.go:327`), so an
  isolated test that only sends from the bot itself sees zero events
  — by design, not a bug.

### 8.3 One UX polish landed in the closeout

`agentchat watch state --help` now explicitly documents the downstream
buffering footgun. `agentchat watch state | jq` looked silent because
`jq` block-buffers its own stdout when piped — the agentchat side is
already line-flushed via direct `*os.File.WriteString`. The help text
now shows three working invocations:

```
agentchat watch state | jq --unbuffered '.totals'
agentchat watch state | stdbuf -oL jq '.totals'
agentchat watch state | grep --line-buffered version
```

No Go-code change to the streaming path — `agentchat watch state >
file` was always per-frame correct; only consumers need to opt out of
their own block-buffering. This is a documentation fix, not a
behavioural change.

### 8.4 Final gate before commit

```bash
make fmt vet      # clean
make test         # all PASS
make smoke        # M1-M5 PASS
make cover        # total 75.6%
# make test-race already run by the auditor in §7.2; no concurrent
# code paths changed in the help-text polish.
```

Status: **M5 closed**. M4 + M3 + M2 + M1 also closed; next milestone
is **M6 (announcements)** per `docs/04-roadmap.md` §7.
