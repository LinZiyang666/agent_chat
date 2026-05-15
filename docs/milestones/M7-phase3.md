# M7 — Phase 3 Review

Date: 2026-05-14

Current Verdict: **PASS** (after Resolution round 2 re-audit)

Initial Verdict: **FAIL**

M7 的附件主线已经落地，开发侧 happy-path 测试和 smoke 能通过。但正式审查补了两个边界测试，均失败：

1. 未授权用户在 room membership 检查之前就能触发附件文件 stat / 大小检查。
2. 25 MB 限制按“整条消息附件总和”执行，而需求写的是“每个文件”。

这两个问题修复并重跑 gate 前，M7 不建议关闭。

## Scope Reviewed

- `docs/02-requirements-final.md` §3.4 / §5.5
- `docs/04-roadmap.md` §8 M7 scope
- `docs/milestones/M7-phase1.md`
- `docs/milestones/M7-phase2.md`
- M7 implementation diff: migration, attachment repo, downloader, send/list API, Discord adapter, CLI, client, smoke

## Findings

### M7-P3-001 — Blocker — Attachment preflight runs before room authorization

`SendMessage` currently stats attachment paths and enforces size limits before resolving the room and checking whether a `role=user` caller belongs to that room:

- `internal/api/v1/messages.go` lines 83–125: `os.Stat`, directory check, and aggregate size check.
- `internal/api/v1/messages.go` lines 146–168: room / role / membership gate happens later.

This creates a filesystem oracle for any authenticated token. A non-member can distinguish:

- nonexistent daemon-local paths: `400 INVALID_ARGUMENT`
- existing huge files: `413 ATTACHMENT_TOO_LARGE`
- other cases: provider / membership-dependent errors

The smoke script currently encodes this behavior by posting to `no-such-room` and expecting the size guard to fire before room lookup. That is the wrong boundary for a user-facing API that reads daemon-local files.

Repro test added:

- `internal/api/m7_phase3_audit_test.go`

Command:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run TestPhase3AttachmentAuthzRunsBeforeSizePreflight -count=1 -v
```

Observed:

```text
=== RUN   TestPhase3AttachmentAuthzRunsBeforeSizePreflight
    m7_phase3_audit_test.go:30:
        expected: 403
        actual  : 413
        {"error":{"code":"ATTACHMENT_TOO_LARGE","message":"attachment aggregate exceeds Discord 25MB limit (27262976 bytes)"}}
--- FAIL: TestPhase3AttachmentAuthzRunsBeforeSizePreflight (1.71s)
```

Impact:

- Authenticated but unauthorized users can probe daemon-readable filesystem paths and sizes.
- Invalid room IDs can return attachment errors instead of room / permission errors.
- The current smoke test will need updating after the fix because it asserts the leaked ordering.

Recommendation:

- Split validation into cheap request-shape validation first (`content` / empty attachment list / path field present), then resolve room + role + membership authorization, then run file stat / size checks, then acquire Provider.
- Preserve the useful “bad attachment beats offline provider” behavior only after authorization has succeeded.
- Update `e2e/m7-smoke.sh` to use a real authorized room for `ATTACHMENT_TOO_LARGE`, not `no-such-room`.

### M7-P3-002 — Major — 25 MB limit is aggregate, but requirements say per file

The final requirements state the Discord free limit as `~25MB / 文件` and say oversize sending should fail directly. The current implementation sums all attachments in one request and rejects when the aggregate exceeds 25 MiB:

- `docs/02-requirements-final.md` §3.4: “当前 ~25MB / 文件”.
- `internal/api/v1/messages.go` lines 90–113: `totalSize += sz` then compare aggregate to `DiscordAttachmentLimit`.
- `internal/bot/types.go` comments and M7 phase1 docs also describe this as a per-message / aggregate cap; those docs now conflict with the final requirements.

Repro test added:

- `internal/api/m7_phase3_audit_test.go`

Command:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run TestPhase3AttachmentLimitIsPerFileNotAggregate -count=1 -v
```

Observed:

```text
=== RUN   TestPhase3AttachmentLimitIsPerFileNotAggregate
    m7_phase3_audit_test.go:51:
        expected: 201
        actual  : 413
        {"error":{"code":"ATTACHMENT_TOO_LARGE","message":"attachment aggregate exceeds Discord 25MB limit (29360128 bytes)"}}
--- FAIL: TestPhase3AttachmentLimitIsPerFileNotAggregate (1.05s)
```

Impact:

- A valid message with two files under 25 MiB each is rejected if the combined size exceeds 25 MiB.
- This narrows the M7 feature beyond the stated requirement and CLI user expectation.

Recommendation:

- Enforce `fi.Size() <= DiscordAttachmentLimit` per attachment.
- If product intentionally wants an aggregate limit, update `docs/02-requirements-final.md`, `docs/04-roadmap.md`, CLI help, comments, and phase docs to state that explicitly.
- Add regression tests for:
  - single file >25 MiB fails with `ATTACHMENT_TOO_LARGE`;
  - multiple files each <=25 MiB can pass even when combined size >25 MiB;
  - one oversized file among multiple files fails.

### M7-P3-003 — Minor — Downloader has retry but no backoff

Roadmap §8 calls for “失败重试 + 退避”. The downloader retries every fixed 2 seconds:

- `internal/attachment/downloader.go` lines 126–136: fixed ticker.
- `internal/attachment/downloader.go` lines 151–156: failed rows stay pending and retry next cycle.

This is acceptable for small local testing, but permanent failures such as expired CDN URLs or oversized rows will be retried and logged every cycle. It is not a blocker for M7 if the product accepts the simpler poller, but the roadmap should be updated or a minimal backoff marker should be added.

Recommendation:

- Add retry metadata (`attempts`, `next_attempt_at`, `last_error`) or document fixed-interval retry as an intentional M7 simplification.

## Passing Baseline Checks

Existing M7 tests pass when the new phase3 audit tests are excluded:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run 'TestSendMessageWithAttachmentPersistsRow|TestSendMessageAttachmentTooLargeReturns413|TestSendMessageAttachmentMissingPathReturns400|TestListMessagesHydratesAttachments' -count=1 -v
env GOCACHE=/tmp/agentchat-gocache go test ./internal/attachment -count=1 -v
env GOCACHE=/tmp/agentchat-gocache bash e2e/m7-smoke.sh
env GOCACHE=/tmp/agentchat-gocache go vet ./...
gofmt -l internal cmd pkg
git diff --check
```

Results: PASS / clean.

I did not run full `test-race` or coverage after adding the phase3 tests because the targeted tests are red; the full gate cannot pass until M7-P3-001 and M7-P3-002 are fixed.

## Questions / Notes

- M7 intentionally skips HEAD pre-check and uses `LimitReader` instead. I accept this as a defensible simplification if it stays documented.
- The downloader path is `<attachments>/<message-id>/<attachment-id>/<filename>`, not the roadmap’s `<room-id>/<message-id>/<filename>`. This is safer for same-name files, but the roadmap/demo should be reconciled.
- Outbound attachment rows do not compute `sha256`; inbound downloaded rows do. If `sha256` is meant to be part of the durable index contract, outbound should compute it too. If not, document that it is download-cache metadata only.

## Resolution (developer pass, 2026-05-14)

> Audit findings addressed by the developer. Re-audit is the user's
> call; do NOT mark Phase 3 PASS from inside this section.

### M7-P3-001 — Resolved

**Decision:** attachment validation (`os.Stat`, directory check, size
guard) runs AFTER the room-membership authorization, never before.
The original phase1 §3.3 "size guard first" reasoning is reversed —
the information leak (path existence + size oracle to any
authenticated token) outweighs the cheap-rejection benefit.

**Code change:** `internal/api/v1/messages.go` `SendMessage` handler
re-ordered. New sequence:

1. JSON decode + request-shape validation (content/attachments
   not both empty, priority valid).
2. Actor extraction from context.
3. `WithTx`: resolve room (exists + not archived), check
   member-or-admin, resolve reply target.
4. Per-attachment: `os.Stat`, directory rejection, per-file
   `<= DiscordAttachmentLimit` (see M7-P3-002 below).
5. `providerForActor` (acquire bot).
6. `Provider.SendMessage` (the slow Discord call).

**Tests:**
- `TestPhase3AttachmentAuthzRunsBeforeSizePreflight` (auditor's
  test) — **now PASS**: outsider with a 26 MB attachment returns
  403 PERM_DENIED, NOT 413.
- Existing `TestSendMessageAttachmentMissingPathReturns400` and
  `TestSendMessageAttachmentTooLargeReturns413` still pass because
  they use the admin token + a valid room, so authz succeeds before
  the stat/size guard fires.

**Smoke update:** `e2e/m7-smoke.sh` previously asserted
`POST /v1/rooms/no-such-room/messages` returned 413 / 400. After the
re-order, the only safe answer is 404. The smoke now asserts
NOT_FOUND for the unknown-room path; the size+stat branches are
verified by the Go integration tests (cited inline in the smoke).

### M7-P3-002 — Resolved

**Decision:** per-file size check. The 25 MB ceiling applies to each
attachment individually; a request with two 14 MB files is accepted
(aggregate 28 MB, no single file oversize). Matches
`02-requirements-final.md` §3.4 ("~25MB / 文件") and the roadmap
demo wording ("file exceeds Discord 25MB limit").

**Code change:** `internal/api/v1/messages.go` — replaced the
`totalSize += sz` aggregate accumulator with a single `if sz >
DiscordAttachmentLimit` check inside the per-attachment loop. Error
message now names the offending file explicitly.

**Tests:**
- `TestPhase3AttachmentLimitIsPerFileNotAggregate` (auditor's
  test) — **now PASS**: two 14 MiB files → 201 Created.
- New `TestSendMessageSingleOversizeAttachmentFails` — a single 26
  MiB file still triggers 413.
- New `TestSendMessageMixedOneOversizeAttachmentFails` — one large
  + one small still fails when the large one is oversize.
- Existing `TestSendMessageAttachmentTooLargeReturns413` continues
  to assert the single-file path.

**Doc updates:**
- `docs/milestones/M7-phase1.md` §3 / §4 will be re-stamped to say
  "per-file" not "aggregate". (`M7-phase2.md` had it correct in §5
  open questions; the phase1 wording was the drift.)

### M7-P3-003 — Resolved

**Decision:** add an in-process backoff to the downloader. The
schedule doubles (2 s → 4 s → 8 s → 16 s → 32 s → 64 s → 120 s and
caps at 120 s). State resets on daemon restart, which is
acceptable — the first attempt after restart counts as attempt 1.
Backoff state lives in `Downloader.failed` keyed by attachment id;
successful fetches `delete(failed, id)`.

**Code change:** `internal/attachment/downloader.go` — new
`failState` map + helpers (`inBackoff`, `recordFailure`,
`clearFailure`). The poll loop short-circuits when a row is still
in its backoff window. The WARN log now carries `attempts` and
`next_retry_in` so an operator can see the pace.

**Tests:**
- New `TestDownloaderBacksOffFailedRow` drives a fake `time.Now()`:
  first cycle hits the HTTP server once (and records attempts=1),
  second cycle at the same instant does NOT hit (backoff window
  open), third cycle at base + 3s DOES hit again.
- Existing tests (`TestDownloaderFetchesPendingRow`,
  `TestDownloaderSkipsOversizeRow`) still pass.

**No schema change** was needed: backoff is process-memory only. A
future milestone wanting persistent retry counters can add
`attempts INTEGER` + `next_attempt_at INTEGER` columns to
`attachments` without breaking anything.

### Note resolutions

- **HEAD pre-check skipped** (auditor's first note): documented as
  intentional in M7-phase1.md §6 known UX trade-offs.
- **Path layout `<msg-id>/<att-id>/<filename>`** (auditor's
  second note): kept as-is for same-name-file safety. Roadmap §8's
  `<room-id>/<msg-id>/<filename>` proposal is superseded;
  `M7-phase1.md` §3.1 / §6 already documents the chosen layout and
  why. The roadmap will be reconciled on the next docs pass.
- **sha256 asymmetry** (outbound skip, inbound compute) (auditor's
  third note): outbound sources already exist on local disk and the
  caller has whatever hash they want; we don't gain integrity
  signal by re-hashing the user's own bytes. Inbound hashing serves
  the "did this CDN response match what we asked for" check —
  callers can re-verify against a trusted hash. Now explicitly
  documented in `M7-phase1.md` §3.1.

### Coverage delta after fixes

```
ok  internal/api                10 tests cover the M7 surface (4 baseline +
                                2 audit + 2 per-file regressions +
                                2 list / mention combinations) — all PASS
ok  internal/attachment         3 tests (fetch + backoff + oversize) — all PASS
```

Phase 1+2 + phase 3 gap-fill code now resides on the main worktree.
User decides whether phase 3 passes on re-audit.

## Re-Review (auditor pass, 2026-05-14)

Verdict: **FAIL**

The two original API findings are fixed and the original auditor tests now pass. The downloader backoff fix is only partially resolved: it adds process-memory backoff, but the cap calculation is not safe for high retry counts.

### Resolved findings

- **M7-P3-001 — Resolved.** `SendMessage` now performs room lookup and membership / admin authorization before `os.Stat` and size validation. `TestPhase3AttachmentAuthzRunsBeforeSizePreflight` passes.
- **M7-P3-002 — Resolved.** The 25 MiB limit is now per attachment, not aggregate. `TestPhase3AttachmentLimitIsPerFileNotAggregate`, the single-oversize regression, and the mixed-oversize regression pass.

I also aligned stale wording found during re-review:

- `internal/api/v1/messages.go`
- `internal/api/v1/types.go`
- `internal/bot/types.go`
- `cmd/agentchat/cmds/send.go`
- `docs/milestones/M7-phase1.md`
- `docs/milestones/M7-phase2.md`

### M7-P3-004 — Minor — Backoff cap overflows / wraps for high retry counts

`internal/attachment/downloader.go` implements:

```go
base := time.Second * time.Duration(1<<uint(attempts))
const cap = 120 * time.Second
if base > cap {
	return cap
}
return base
```

This caps correctly for small values, but after enough failures the shift can overflow / wrap before the cap comparison. A permanently failing attachment can eventually return `0s` instead of `120s`, which defeats the purpose of M7-P3-003 for long-running daemons.

Regression test added:

- `internal/attachment/downloader_test.go`: `TestBackoffForCapsAtTwoMinutes`

Failing command:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/attachment -count=1 -v
```

Observed:

```text
=== RUN   TestBackoffForCapsAtTwoMinutes
    downloader_test.go:154: expected: 2m0s actual: 0s attempts=100
--- FAIL: TestBackoffForCapsAtTwoMinutes
FAIL github.com/LinZiyang666/agentchat/internal/attachment
```

Recommended fix:

- Cap before shifting, for example `if attempts >= 7 { return 120 * time.Second }`, then compute the shifted duration only for small attempt counts.
- Keep `TestBackoffForCapsAtTwoMinutes` as the regression.

### Re-review verification

Passing:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run 'TestPhase3AttachmentAuthzRunsBeforeSizePreflight|TestPhase3AttachmentLimitIsPerFileNotAggregate|TestSendMessageSingleOversizeAttachmentFails|TestSendMessageMixedOneOversizeAttachmentFails|TestSendMessageAttachmentTooLargeReturns413|TestSendMessageAttachmentMissingPathReturns400' -count=1 -v
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run 'TestPhase3Attachment|TestSendMessage.*Attachment|TestListMessagesHydratesAttachments' -count=1
env GOCACHE=/tmp/agentchat-gocache bash e2e/m7-smoke.sh
env GOCACHE=/tmp/agentchat-gocache go vet ./...
gofmt -l internal cmd pkg
git diff --check
```

Failing:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/attachment -count=1 -v
```

I did not run full race / coverage after this because the targeted downloader package is red.

## Resolution round 2 (developer pass, 2026-05-14)

### M7-P3-004 — Resolved

**Decision:** cap the attempt count BEFORE the shift, exactly as the
auditor recommended. The pathology is the Go shift spec: `1 << 100`
on a signed `1` shifts past int's bit width and yields 0; the
subsequent `0 > cap` check is false and the function returns 0,
collapsing the backoff at exactly the point where a row is most
likely permanently broken.

**Code change:** `internal/attachment/downloader.go` — `backoffFor`:

```go
func backoffFor(attempts int) time.Duration {
    if attempts <= 0 {
        return 0
    }
    const cap = 120 * time.Second
    if attempts >= 7 {        // ← cap before shift (was: shift then cap)
        return cap
    }
    base := time.Second * time.Duration(1<<uint(attempts))
    if base > cap {           // defensive — 2^7 = 128 > 120, still clamp
        return cap
    }
    return base
}
```

7 is chosen because `2^7 s = 128 s` is the first value past the 120 s
ceiling, so the explicit-cap branch only takes effect for inputs
that the original schedule would have clamped anyway.

**Tests:**
- `TestBackoffForCapsAtTwoMinutes` (auditor's test, `attempts ∈
  {7, 8, 30, 100}`) — **now PASS**.
- Existing `TestDownloaderBacksOffFailedRow` still PASS (it exercises
  the small-attempt branch `attempts=1`, schedule = 2 s).
- Existing `TestDownloaderFetchesPendingRow` /
  `TestDownloaderSkipsOversizeRow` still PASS.

**Gate after round 2:**

```
go build ./...                       clean
go vet ./...                         clean
gofmt -l internal cmd pkg            clean
go test ./internal/attachment        4/4 PASS
```

Other packages were not re-run because round 2 touched only
`internal/attachment/downloader.go::backoffFor`.

## Final Re-Review (auditor pass, 2026-05-14)

Verdict: **PASS**

M7-P3-004 is fixed. `backoffFor` now caps `attempts >= 7` before doing the shift, so high retry counts cannot overflow / wrap into a zero-duration retry loop. The auditor regression `TestBackoffForCapsAtTwoMinutes` is now green.

Final finding status:

- **M7-P3-001 — Resolved.** Authz runs before attachment stat / size preflight.
- **M7-P3-002 — Resolved.** Discord 25 MiB limit is enforced per file, not aggregate.
- **M7-P3-003 — Resolved.** Downloader has process-memory retry backoff.
- **M7-P3-004 — Resolved.** Backoff cap is applied before the shift.

Verification run by auditor:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/attachment -count=1 -v
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run 'TestPhase3AttachmentAuthzRunsBeforeSizePreflight|TestPhase3AttachmentLimitIsPerFileNotAggregate|TestSendMessageSingleOversizeAttachmentFails|TestSendMessageMixedOneOversizeAttachmentFails|TestSendMessageAttachmentTooLargeReturns413|TestSendMessageAttachmentMissingPathReturns400' -count=1 -v
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api ./internal/attachment ./pkg/client -count=1
env GOCACHE=/tmp/agentchat-gocache bash e2e/m7-smoke.sh
env GOCACHE=/tmp/agentchat-gocache go build ./...
env GOCACHE=/tmp/agentchat-gocache go vet ./...
gofmt -l internal cmd pkg
git diff --check
```

Results: all PASS / clean.

I did not run full `test-race` or coverage in this final re-review. The M7-specific blocker / major / retry findings are closed from the auditor side.

## Resolution round 3 — `DiscordAttachmentLimit` lowered 25 MB → 10 MB

**Decision (user-driven, post-PASS):** Discord lowered the
free-tier per-file upload cap from 25 MB to 10 MB in 2024-09. The
M7 implementation matched the old 25 MB value because the
roadmap / requirements were written against the older Discord
limit. Real-Discord testing today surfaced the gap (a 2 × 14 MiB
message passed agentchat's per-file 25 MB guard, then was 503-ed
by Discord). The user picked option A from the in-chat menu:
update the constant to 10 MB.

**Code change:** `internal/api/v1/messages.go::DiscordAttachmentLimit`
now `10 * 1024 * 1024`. Comment explains the lowering + the
historical 25 MB value. Error message rewritten to print the
actual cap dynamically (no more `25MB` literal).

**Doc / comment updates:** `internal/errcode/errcode.go`,
`internal/bot/discord/discord.go`, `internal/api/v1/types.go`,
`cmd/agentchat/cmds/send.go --attach help text`, `M7-phase1.md`
Goal / §3.3 / auth matrix — all reference the new value with a
pointer to the constant.

**Test fixture update:** The auditor's
`TestPhase3AttachmentLimitIsPerFileNotAggregate` originally used
2 × 14 MiB files (each < 25 MB, aggregate > 25 MB). With the
limit lowered to 10 MB, those values would *correctly* reject as
per-file oversize, defeating the test's intent. Fixtures
rebracketed to 2 × 6 MiB (each < 10 MB, aggregate 12 MiB > 10 MB)
so the per-file vs aggregate distinction still loads. Inline
comment in the test explains the adjustment. Other audit
fixtures (26 MiB single-file oversize) still trigger oversize
under the new value — no change needed.

**Not touched:** `docs/02-requirements-final.md` §3.4 still says
"~25 MB / 文件". This is a baseline product doc; the user can
choose to reconcile it (e.g., "~10 MB / 文件 on free Discord
servers as of 2024-09") in a separate pass.

**Operator escape hatch:** boosted Discord guilds support
50 MB / 100 MB per file; Nitro callers go higher. Operators on
such servers can patch `DiscordAttachmentLimit` locally. A
config-driven knob (option C in the original menu) was discussed
but not adopted in this round — the simpler floor matches what
99 % of self-hosted operators will hit first.

**Gate after round 3:**

```
go build ./...                          clean
gofmt -l internal cmd pkg               clean
go test ./internal/api ./internal/...   PASS (incl. auditor's
                                              re-bracketed test)
bash e2e/m7-smoke.sh                    PASS
```
