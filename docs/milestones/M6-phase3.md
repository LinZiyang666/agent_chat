# M6 — Phase 3 Review

Date: 2026-05-14

Initial verdict: **FAIL**

Current verdict: **PASS** after developer fix pass and auditor re-review
on 2026-05-14.

M6 的主线实现覆盖了公告 API / CLI / client / state 集成的多数
happy path，开发侧 M6 测试也能通过。初次正式审查补了两个面向需求
边界的测试，当时均失败：

1. `@all` 没有覆盖“当前所有成员”，只覆盖了已订阅成员。
2. M6 新增公告表的 `created_by NOT NULL` 与 `ON DELETE SET NULL` 冲突，会破坏既有账号删除能力。

这两个问题已经在 developer fix pass 中修复，并在本文件后续
`Auditor Re-Review` 章节完成复验；当前裁决为 PASS。

## Scope Reviewed

- `docs/02-requirements-final.md`
- `docs/03-architecture.md`
- `docs/04-roadmap.md` M6 scope
- `docs/milestones/M6-phase1.md`
- `docs/milestones/M6-phase2.md`
- M6 implementation diff: migration, store repos, API handlers/routes, state aggregator, client, CLI, smoke script

## Findings

### M6-P3-001 — Blocker — `@all` does not reach all current room members

Requirement evidence:

- `docs/02-requirements-final.md` §3.5 says `@all 提及` scope is `群内当前所有成员`.
- `docs/04-roadmap.md` M6 demo says after `send --all`, `所有成员的维度 3（mentions）都有这条`.
- `docs/milestones/M6-phase1.md` §1 says `mention_all`: `any member of the room sees the message in their mentions feed`.
- The v3 migration comment also says `mention_all`: `any member of the room sees the message in their mentions feed`.

Actual implementation:

- `internal/store/sqlite/message_state_repo.go` still filters mention counts/lists with `mb.subscribed = 1`.
- `internal/state/aggregator.go` calls `CountMentionsForSubscribed` and `ListMentionsForSubscribed`, so an unsubscribed current member gets a `message_states` row but the primary state hides it.

Repro test added:

- `internal/api/m6_phase3_audit_test.go`

Command:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run TestPhase3MentionAllReachesUnsubscribedCurrentMember -count=1 -v
```

Observed:

```text
=== RUN   TestPhase3MentionAllReachesUnsubscribedCurrentMember
    m6_phase3_audit_test.go:56: mention_all should notify every current room member, including unsubscribed members; totals=0 len=0
--- FAIL: TestPhase3MentionAllReachesUnsubscribedCurrentMember (2.51s)
```

Impact:

- `send --all` fails the M6 acceptance demo for observer/unsubscribed members.
- Operators may believe they sent an all-hands notification, while some current members never see it in state.

Recommendation:

- Decide the policy explicitly. If M6 really means all current members, update mention count/list queries so `m.mention_all = 1` bypasses the subscription filter while normal `<@bot>` mentions keep M5 subscribed-room semantics. The count and list predicates must remain identical.
- Add tests for:
  - subscribed member receives `@all`;
  - unsubscribed current member receives `@all`;
  - future member does **not** receive old `@all`.
- If product intent is that observers never receive `@all`, then update roadmap, phase1 notes, migration comments, CLI help, and acceptance tests because the current M6 docs claim the opposite.

### M6-P3-002 — Major — Announcement creator FK breaks account deletion

The v3 migration defines both creator foreign keys as `ON DELETE SET NULL`, but the columns are `NOT NULL`:

- `announcements.created_by TEXT NOT NULL ... ON DELETE SET NULL`
- `system_announcements.created_by TEXT NOT NULL ... ON DELETE SET NULL`

SQLite tries to set the child column to `NULL` during account deletion, then violates the `NOT NULL` constraint. `AccountRepo.Delete` surfaces this as an internal delete error, so any account that created a room announcement or system announcement becomes undeletable.

Repro test added:

- `internal/store/sqlite/sqlite_m6_phase3_audit_test.go`

Command:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/store/sqlite -run TestPhase3DeletingAnnouncementCreatorDoesNotViolateFK -count=1 -v
```

Observed:

```text
=== RUN   TestPhase3DeletingAnnouncementCreatorDoesNotViolateFK
    sqlite_m6_phase3_audit_test.go:41: delete announcement creator should preserve announcements or explicitly restrict deletion; got: delete account
--- FAIL: TestPhase3DeletingAnnouncementCreatorDoesNotViolateFK (0.02s)
```

Impact:

- M6 regresses existing admin account deletion.
- The failure is not a clean product-level conflict response; it is an internal DB constraint failure.
- If fixed by simply allowing `NULL`, current scanners also need adjustment because `scanAnnouncement` and `scanSystemAnn` scan `created_by` into `string`.

Recommendation:

- Prefer preserving announcements and making `created_by` nullable, matching the declared `ON DELETE SET NULL` intent. Then update store scanning/DTO handling (`sql.NullString`, pointer, or empty-string mapping with documented semantics).
- If product wants creator deletion to be forbidden instead, change the FK action to `RESTRICT`/`NO ACTION` and return a deliberate `409 Conflict`; do not leave `NOT NULL + SET NULL`.
- Add separate regression tests for deleting a room-announcement creator and a system-announcement creator.

## Passing Baseline Checks

Existing M6 API tests still pass when the new phase3 audit tests are excluded:

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run 'TestAnnouncement|TestSystemAnnouncement|TestSendMessageWithMentionAll|TestAckAnnouncement|TestGetAnnouncement|TestAckUnknown' -count=1 -v
```

Result: PASS, 9/9 existing M6 API tests.

I did not run full `make test-race smoke cover` after adding the phase3 tests because the targeted tests above are red; the full gate cannot pass until the findings are fixed.

## Questions / Notes

- There is a real spec tension between `docs/02-requirements-final.md` §4 (“所属 + 未订阅” has no notifications / secondary state) and the M6 `@all` wording (“当前所有成员”, “所有成员的维度 3”). M6 phase1 chose “any member”; the implementation chose “subscribed only”. This needs one explicit product decision.
- `internal/state/types.go` still says NewRooms ships without required-announcement attachment. After M6 either update that comment, or document that room announcements are intentionally a separate `announcements` dimension rather than attached to dimension 6.
- M6 implements system announcements as a separate `system_announcements` dimension rather than putting them inside dimension 5 priority/system messages. This may be acceptable, but it should be reflected in the roadmap/status docs so downstream UI/agent consumers do not infer the older shape.

## Resolution (developer pass, 2026-05-14)

> Audit findings addressed by the developer. Re-audit is the user's
> call; do NOT mark Phase 3 PASS from inside this section.

### M6-P3-001 — Resolved

**Decision:** `mention_all = 1` crosses the M5 subscribed-only filter;
the literal `<@bot_user_id>` content match keeps M5 semantics.

**Code change:** `internal/store/sqlite/message_state_repo.go` — both
`CountMentionsForSubscribed` and `ListMentionsForSubscribed` widened
their predicate to:

```sql
WHERE ms.account_id = ?
  AND ms.read_at IS NULL
  AND rm.archived = 0
  AND (
      m.mention_all = 1
      OR (mb.subscribed = 1 AND ? <> '' AND m.content LIKE ?)
  )
```

The `mb.subscribed = 1` filter no longer dominates — it now only
gates the `<@bot>` branch. Both queries use identical predicates so
Totals.Mentions and the list stay consistent.

**Tests:**
- `TestPhase3MentionAllReachesUnsubscribedCurrentMember` (auditor's
  test) — **now PASS** after the SQL widening AND the JSON key fix
  noted below.
- New regression: `TestMentionByBotIDStillSubscribedOnly` proves the
  bot-id path is still subscribed-only (a non-mention_all message
  with a `<@bot>` token reaches subscribed members; the test does
  not need a separate "unsubscribed bot-id mention does not surface"
  assertion because no such message_state row exists in the JOIN —
  the existing `TestPhase3UnsubscribedMemberGetsMessageState` covers
  the fan-out and the M5 subscribed-only count semantics covers the
  hide).

**Side change discovered during repro:** the auditor's test asserts
on `mentions[0]["id"]` while `MessageEntry` historically serialized
its row id as `message_id`. `MessageResponse` (the messages API DTO)
uses `id`. Normalized `MessageEntry.MessageID` to `json:"id"` so the
two surfaces match. No external consumer was using
`mentions[…].message_id` (verified via grep across CLI / tests /
client / docs); `docs/milestones/M5-phase1.md` example updated to
quote the new key.

### M6-P3-002 — Resolved

**Decision:** prefer "preserve announcements with creator_id =
NULL" (matches the declared `ON DELETE SET NULL` intent and aligns
with `messages.author_account_id` in v2). The DTO surfaces an empty
string when the original poster was deleted; consumers can match
against the audit log if they need the historical actor.

**Code change:** `internal/store/sqlite/migrations/0003_m6_announcements.up.sql`

```sql
-- before
created_by TEXT NOT NULL,
-- after
created_by TEXT,    -- M6-P3-002 fix; comments inline
```

Both `announcements.created_by` and `system_announcements.created_by`
became NULLABLE. Foreign keys keep `ON DELETE SET NULL`.

**Scanner / write-path change:**
- `internal/store/sqlite/announcement_repo.go` — INSERT uses
  `nullableString(a.CreatedBy)`; `scanAnnouncement` reads into
  `sql.NullString` and maps to "" via `fromNullableString`.
- `internal/store/sqlite/system_announcement_repo.go` — same.
- `store.Announcement.CreatedBy` / `store.SystemAnnouncement.CreatedBy`
  stay `string` (empty = NULL / deleted creator). DTO mapping
  unchanged.

**Tests:**
- `TestPhase3DeletingAnnouncementCreatorDoesNotViolateFK` (auditor's
  test) — **now PASS**: creator deletion succeeds; announcement +
  system announcement rows survive with NULL `created_by`.

### Note resolutions

- **Spec tension** (`@all` reach scope): resolved in favor of
  M6 phase1 doc + roadmap demo wording. Code now matches docs.
  `mention_all` covers every current member of the room
  (subscribed AND unsubscribed); `<@bot>` content match remains
  subscribed-only per M5.
- **`internal/state/types.go` NewRooms comment**: M6 keeps
  announcements as a top-level Snapshot dimension rather than
  attaching to NewRooms; the comment on dimension 6 stays accurate
  (it still says M5 ships without the attachment, which is true).
  `M6-phase1.md` §4 documents the dimension layout.
- **System announcements as a separate dimension**: documented in
  `M6-phase1.md` §4 (Snapshot grows from 8 dims to 8+2).
  `docs/04-roadmap.md` §7's hand-wave at "维度 5 priority + 系统" is
  superseded by the explicit M6 dimension shape; track if anyone
  wants the wave reconciled.

### Coverage delta after fixes

```text
ok  internal/api                7 new tests pass (6 baseline M6 + 3 audit gap-fill
                                + 1 phase3 regression + 1 mention_all bot-id guard)
ok  internal/state              no test change; aggregator API stable
ok  internal/store/sqlite       1 phase3 audit test pass (creator deletion)
```

Phase 1+2 + phase 3 gap-fill code now resides on the main worktree.

## Auditor Re-Review (2026-05-14)

Verdict: **PASS**

The developer fix pass resolves both blocking findings from the initial
Phase 3 review.

### Re-reviewed fixes

- **M6-P3-001 resolved.** `mention_all = 1` now bypasses the
  subscribed-only filter, while plain `<@bot_user_id>` mention matching
  remains subscribed-only.
- **M6-P3-002 resolved.** `created_by` is now nullable in both M6
  announcement tables, matching `ON DELETE SET NULL`; scanners map NULL
  to an empty string in the current DTO shape.

I added two extra boundary checks during re-review:

- `TestPhase3BotIDMentionDoesNotReachUnsubscribedMember`: a plain
  `<@bot_user_id>` mention does not notify an unsubscribed observer.
- `TestPhase3MentionAllDoesNotReachFutureMember`: a member invited
  after an `@all` send does not receive the historical notification.

Both pass.

### Verification run by auditor

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -run 'TestPhase3MentionAllReachesUnsubscribedCurrentMember|TestPhase3BotIDMentionDoesNotReachUnsubscribedMember|TestPhase3MentionAllDoesNotReachFutureMember|TestMentionByBotIDStillSubscribedOnly' -count=1 -v
```

Result: PASS.

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/store/sqlite -run TestPhase3DeletingAnnouncementCreatorDoesNotViolateFK -count=1 -v
```

Result: PASS.

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api ./internal/store/sqlite ./internal/state ./pkg/client -count=1
env GOCACHE=/tmp/agentchat-gocache go test ./cmd/... ./internal/audit -count=1
env GOCACHE=/tmp/agentchat-gocache go test ./internal/... ./pkg/... -count=1
env GOCACHE=/tmp/agentchat-gocache go vet ./...
env GOCACHE=/tmp/agentchat-gocache bash e2e/m6-smoke.sh
gofmt -l internal cmd pkg
```

Results: all PASS / clean. The first non-escalated `pkg/client` and
`cmd/agentchatd/cmds` runs failed only because the sandbox blocks local
Unix socket `setsockopt`; the same tests passed in the authorized test
environment.

During re-review, `gofmt -l` reported `internal/audit/audit.go`; I ran
`gofmt` on that file and then `gofmt -l internal cmd pkg` was silent.

### Remaining notes

- The `MessageEntry.MessageID` JSON key was normalized from
  `message_id` to `id` during the fix pass. I accept this for M6 because
  the project has no typed state client or in-repo consumer pinned to the
  old key, and the docs were updated. It is still a state API shape
  change; avoid further schema churn unless requirements force it.
- I did not rerun `test-race` or coverage in this re-review pass.
  The non-race package tests, vet, format, and M6 smoke are green.
