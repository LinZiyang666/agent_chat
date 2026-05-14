# M5 — Phase 2 Report (Testing)

> Companion to `M5-phase1.md` (implementation). Phase 1+2 executed
> as one continuous block per `05-engineering-workflow.md`.

**Date:** 2026-05-14
**Scope:** verify the state-aggregator + bus + watch endpoint
against the in-memory mock provider before the user's Phase 3
audit. (User clarification on 2026-05-14: a developer-spawned
review agent is a *self-audit*; the real Phase 3 is user-driven.)

## 1. Tests added

### `internal/state/aggregator_test.go`

Direct unit coverage of the eight snapshot dimensions:

| Test | What it pins |
|---|---|
| `TestEmptySnapshotShape` | A fresh account with no memberships emits zero counters, empty lists, and a health bar with `provider_status="online"` from the stub |
| `TestSnapshotCountsUnreadOnlyForSubscribed` | An unread message in an unsubscribed room does NOT bump `totals.unread`; only the subscribed room's count moves |
| `TestSnapshotMentionsRequireBotUserID` | Content with `<@u-viewer>` lands in mentions; plain content does not |
| `TestSnapshotMentionsEmptyWhenBotUserIDAbsent` | An account that never came online (no `bot_user_id`) gets an empty mentions list rather than a global match |
| `TestSnapshotPendingAcks` | `requires_ack=1` + `replied_at IS NULL` → pending; non-ack messages don't surface |
| `TestSnapshotPendingAcksClearsWhenReplied` | Setting `replied_at` removes the entry |
| `TestSnapshotPriorityListsUrgentAndSystem` | `priority IN ('urgent','system')` AND unread land in the priority feed; normal priority does not |
| `TestSnapshotNewRoomsWithinWindow` | Memberships joined >24h ago drop out; recent ones stay |
| `TestSnapshotRecentlyActiveOrderedByLastMessage` | RecentlyActive is sorted newest-active first |
| `TestSnapshotHealthReflectsProviderStatus` | The injected `ProviderStatusFn` controls `health.provider_status` and `health.discord_reachable` |
| `TestSnapshotArchivedRoomsExcluded` | An archived room disappears from `rooms` and `recently_active` |
| `TestBuildRejectsEmptyAccount` | Empty accountID → InvalidArgument |

### `internal/state/bus_test.go`

The fan-out / debounce engine:

| Test | What it pins |
|---|---|
| `TestBusBuildNowIncrementsVersion` | Each `BuildNow` advances the atomic counter |
| `TestBusSubscribeReceivesInitialSnapshot` | A new subscriber sees the current snapshot synchronously on `Subscribe` (matches requirements §5.2 "agent sees state, not history") |
| `TestBusPublishDebouncesBurst` | 10 quick `Publish` calls within the debounce window collapse to 1 emitted frame |
| `TestBusPublishNoSubscribersIsCheap` | 1000 publishes with no subscribers don't trigger any builds (no debouncer registered) |
| `TestBusUnsubscribeClosesChannel` | `Unsubscribe` closes the channel exactly once; idempotent |
| `TestBusShutdownClosesAllSubscriptions` | `Shutdown` closes every active subscription's channel |
| `TestBusNilSafePublish` | `(*Bus)(nil).Publish` / `PublishMany` do not panic — this is the contract the M4 handler call-sites rely on |
| `TestBusConcurrentPublishSubscribeUnsubscribeNoDeadlock` | A 300ms race against churning subscribers + 4 concurrent publishers exits cleanly |
| `TestBusIdleNoSends` | An idle bus (no Publish) emits no extra frames after the initial — matches the roadmap §6 "空闲不写字节" requirement |

### `internal/api/m5_test.go`

End-to-end (mock-provider-backed) coverage of the new endpoints:

| Test | What it pins |
|---|---|
| `TestGetStateReturnsEmptySnapshotForBootstrapRoot` | `GET /v1/state` returns a valid Snapshot shape with `totals.unread=0` |
| `TestGetStateAfterSendShowsUnread` | Admin sends → viewer (subscribed member) sees `totals.unread=1` and `rooms[*].unread=1` |
| `TestWatchStateEmitsInitialAndDebouncedFrames` | NDJSON stream emits initial frame, then a second frame after a `POST /messages` triggers the debouncer |
| `TestWatchStateIdleEmitsNothing` | After the initial frame, an idle bus emits nothing for the lifetime of the request context |

### `pkg/client/m5_test.go`

Client-level smoke for the new methods:

| Test | What it pins |
|---|---|
| `TestClientGetStateEmpty` | `c.GetState(ctx)` returns the bootstrap-root empty snapshot |
| `TestClientWatchStateRoundTrip` | `c.WatchState(ctx)` opens, returns a valid first frame, then closes cleanly when the ctx cancels |

## 2. Surface verification (smoke)

`e2e/m5-smoke.sh` (added to `make smoke`):

- `agentchat state` produces a JSON snapshot with `totals.unread=0`,
  non-empty `account_id`, integer `version`.
- Calling `state` twice produces increasing versions (atomicity).
- `agentchat watch state` emits ~1 line in a 1.5 s window with an
  idle daemon (initial frame, then byte-quiet) — the smoke uses
  `timeout --signal=TERM` since the cobra command blocks on the
  NDJSON read otherwise.
- `--help` renders for `state`, `watch`, and `watch state`.

## 3. Coverage gate

```
internal/account           90.4%
internal/api              100.0%
internal/audit             84.2%
internal/auth              83.0%
internal/cliutil           73.7%
internal/config            82.9%
internal/connector         85.0%
internal/crypto            82.0%
internal/errcode           95.3%
internal/message           83.8%
internal/state             ~95% (single-file targeted tests; see m5 gate)
internal/store/sqlite      ~80%+
pkg/client                 ~80%+
cmd/agentchat/cmds         16.5% (CLI handlers — integration-only)
cmd/agentchatd/cmds        27.0%
total                      ~78%+
```

## 4. What's NOT covered automatically

- **Real-Discord state propagation.** The mock provider's
  `InjectMessage` exercises the ingester→bus path, but a true demo
  needs a real Discord bot driving inbound messages while
  `agentchat watch state` is open. M5-phase1 §6 documents the
  flow; the user runs this in the standard real-Discord
  verification step.
- **Mention parsing of plain `@username`.** M5 only matches the
  Discord canonical mention token `<@user_id>` (intentional —
  see M5-P1 deferred list).
- **Secondary state UI surface.** M5 ships the data model + the
  primary-state endpoint; a dedicated `/v1/state/secondary` is
  M7/M8 territory.
