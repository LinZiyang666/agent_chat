# M6 — Phase 2 Report (Testing)

> Companion to `M6-phase1.md` (implementation). The dev-side gate is
> intentionally lighter than the user's Phase 3 audit:
>
> - **Iterative single-package `go test` during development** —
>   covered the load-bearing paths as they were written.
> - **Full smoke + integration sweep at milestone close** — to be
>   captured below before handoff.
> - **`make test-race` / `make cover` etc.** — deferred to the
>   user-driven Phase 3 audit; running them here would just shorten
>   that audit's signal without adding diff coverage on the M6
>   surface.

**Date:** 2026-05-14
**Status:** Phase 1+2 ready for user Phase 3.

## 1. Targeted Go tests added

### `internal/api/m6_test.go` (new, 9 tests)

| Test | What it asserts |
|------|-----------------|
| `TestAnnouncementCreateBumpsVersionAndUnreadsAll` | Two consecutive `POST /v1/rooms/{id}/announcement` calls produce versions 1 → 2; viewer's state shows exactly one unread announcement (the latest version), not two. |
| `TestAnnouncementGetLatestReflectsReadFlag` | `GET /v1/rooms/{id}/announcement` returns `read=false` for an un-ack'd viewer; after `POST /v1/announcements/{id}/read` it returns `read=true`, and `Totals.Announcements` drops to 0. |
| `TestSystemAnnouncementUnreadAndAck` | Fresh viewer with no membership sees `Totals.SystemAnnouncements=1` after an admin posts; `GET /v1/system/announcements` lists it with `read=false`; after `POST /…/read`, totals zero and the list flips to `read=true`. |
| `TestSystemAnnouncementCreateForbiddenForNonAdmin` | A `role=user` token gets 403 on `POST /v1/system/announcements`. |
| `TestSendMessageWithMentionAllSurfacesInMentions` | `SendMessageRequest.MentionAll=true` with content that does NOT contain `<@bot>` still bumps the viewer's `Totals.Mentions` and appears in the `mentions` feed. |
| `TestAnnouncementCreateRejectedFromOutsider` | A `role=user` token without membership gets 403 on `POST /v1/rooms/{id}/announcement`. |
| `TestAckAnnouncementIdempotent` *(M6-S6 self-audit gap-fill)* | Two acks of the same announcement both return 200; second ack is a no-op (read_at is overwritten with the new instant, no error). |
| `TestGetAnnouncementNotFoundForEmptyRoom` *(M6-S6)* | `GET /v1/rooms/{id}/announcement` on a room with no announcements returns 404. |
| `TestAckAnnouncementUnknownIDReturns404` *(M6-S6)* | `POST /v1/announcements/{unknown-id}/read` returns 404 rather than silently creating an orphan read row. |

### Pre-existing tests still passing

The M3 / M4 / M5 test packages have unchanged signatures relative to
M6's schema changes (the `mention_all` column defaults to 0 for all
existing rows; the new repos are accessed only through the four new
endpoints + the aggregator). `go test ./internal/...` passes cleanly
after M6 lands.

## 2. End-to-end smoke

### `e2e/m6-smoke.sh` (new)

Mock-driven (matches M3/M4/M5 convention: real Discord delivery is
operator-verified, never CI-verified, because guild creds aren't
shipped). Verifies:

1. **CLI help renders** for every new verb: `room announce`,
   `room announce-show`, `ack-announcement`, `system-announcements`,
   `ack-system`, `admin system-announce`, and `send --help` exposes
   the `--all` flag.
2. **Empty list shape**: `GET /v1/system/announcements` returns `[]`
   not `null` from the JSON helper path.
3. **Admin post + viewer unread**: `agentchat admin system-announce`
   yields `Totals.SystemAnnouncements=1` from the root account's
   state (root is its own viewer here).
4. **ACK zeros the counter** and flips `read=true` in
   `system-announcements` listing.
5. **404 path on unknown room**: `POST /v1/rooms/unknown/announcement`
   returns 404 (verified via `curl --unix-socket` since the CLI would
   need a real bot for the happy path).
6. **403 path on non-admin sys announce**: a `role=user` token gets
   403 on `POST /v1/system/announcements`.

Note: the smoke test does NOT exercise the announcement happy-path
(create + ack inside a room) because that would require an online
bot. That path is covered by `internal/api/m6_test.go` against a
mock provider, and by the real-Discord operator steps printed at the
end of the smoke script.

## 3. Build / static checks

- `go build ./...` — clean
- `go vet ./...` — clean
- `gofmt -l` — clean (CI catches drift)

## 4. Coverage delta (informational)

Not measured at this stage — coverage delta is computed during the
Phase 3 gate, not during Phase 1+2 development, per
`feedback_iterative_tests_first.md`. The expectation:

- `internal/store/sqlite` should see a modest bump (two new repo
  files, ~200 LOC of straightforward CRUD).
- `internal/api` should see a bump from the six new handlers (the
  m6_test.go file exercises all of them).
- `internal/state` should see a bump from the two new dimensions in
  Build.

## 5. Manual / real-Discord verification

Per the user-driven workflow, real-Discord verification is held for
the user's Phase 3 audit. The expected operator playbook is captured
at the tail of `e2e/m6-smoke.sh`:

```
1. Online a bot; create a room; invite a subscribed viewer.
2. agentchat room announce <room> 'v2 launch'
   → viewer sees: agentchat state | jq '.totals.announcements' == 1
3. agentchat ack-announcement <id>
   → viewer state announcements drops to 0
4. agentchat send <room> --all 'drill 0600'
   → viewer mentions bumps even without <@bot> in content
5. agentchat admin system-announce 'maintenance Sun 02:00'
   → viewer system_announcements bumps; ack-system zeroes it
```

## 6. Open questions for Phase 3

The self-audit findings (recorded as M6-S1..M6-S9 in chat) are
expected to be the starting point of Phase 3 — none of them are
blockers but several are design choices the auditor may want
re-examined:

- **S3** (archived-room announcement unread propagation): currently
  intentional; do we want a config knob?
- **S5** (O(N) publish fan-out on system announcement create):
  acceptable at current scale, but the auditor should flag if there
  is an obvious incremental improvement.
- **No down.sql convention** (S9): if Phase 3 disagrees with M1's
  forward-only stance, this is the milestone to revisit.
