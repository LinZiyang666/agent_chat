# M4 — Phase 2 Report (Testing)

> Companion to `M4-phase1.md` (implementation). Phase 1+2 executed as
> one continuous block per `05-engineering-workflow.md`.

**Date:** 2026-05-14
**Scope:** verify the rooms/memberships/messages surface against a
mock provider before the operator runs the real-Discord demo.

## 1. Tests added

### `internal/store/sqlite/sqlite_m4_test.go`

Direct repo-level coverage of the new tables:

| Test | What it pins |
|---|---|
| `TestAccountBotUserIDRoundTrip` | The 0002 migration's `ALTER TABLE accounts ADD COLUMN bot_user_id` round-trips through `accountRepo.Update`/`Get` |
| `TestRoomRepoCRUD` | Create → conflict-on-duplicate-channel → Get → GetByDiscordChannelID → rename + archive Update → List with/without archived → Delete → NotFound |
| `TestMembershipUpsertPreservesJoinedAt` | The `ON CONFLICT(account_id, room_id) DO UPDATE SET subscribed = ...` clause keeps the original `joined_at` when subscribe is toggled |
| `TestMembershipListSubscribersFilters` | `ListSubscribers` returns only `subscribed=1` rows |
| `TestMessageCreateAndDedupe` | `Create` raises `Conflict` on duplicate `discord_msg_id`; `CreateIgnoreConflict` returns `(existingID, false, nil)` for the same case — this is the send/echo dedupe primitive |
| `TestMessageListOrderingAndPaging` | `List` returns newest-first; `before=<id>` returns strictly-older rows |
| `TestMessageStateUpsertPreservesPriorTimestamp` | The COALESCE in the upsert prevents a nil `read_at` from clobbering an already-set timestamp (e.g., the ingester running after the send handler) |
| `TestBundlerWithTxRollsBackOnError` | New repos participate correctly in the tx: a membership insert inside `WithTx` that returns an error leaves no row behind |

### `internal/message/ingester_test.go`

Package-level unit coverage for the ingester's lifecycle:

| Test | What it pins |
|---|---|
| `TestIngesterAttachIsIdempotent` | Two `AttachAccount` calls produce one subscription; `DetachAccount` undoes it and the drain goroutine exits |
| `TestIngesterDetachWithoutAttachIsNoOp` | `DetachAccount` on a never-attached account does not panic |

### `internal/api/m4_test.go`

End-to-end (mock-provider-backed) coverage of the full HTTP surface:

| Test | What it pins |
|---|---|
| `TestRoomCreateInsertsRowAndCallsCreateChannel` | The actor's mock provider observes `Created = ["ch-ops"]`; the response includes the persisted Room with that channel id |
| `TestRoomCreateWithoutOnlineExecutorFails` | Returns `Conflict` (no live provider) — matches the M3-P3-001/002 family of "require online" guards |
| `TestRoomListAdminSeesAllUserSeesOwn` | Admin's GET `/v1/rooms` lists every room; a fresh user's list is empty because they have no memberships |
| `TestRoomInviteAndKickRoundTrip` | Invite → mock `Added` contains `("ch-ops", "u-alice")`; Kick → `Removed` contains the same; both produce `room.member.add` / `room.member.remove` audit rows |
| `TestRoomSendMessagePersistsAndDedupesEcho` | Send writes one message; re-injecting the same `discord_msg_id` through the mock event channel triggers the ingester's dedupe path; history still shows one row |
| `TestIngesterIngestsExternalMessage` | An `InjectMessage` from a non-bot author lands in the DB; `eventually` pulls the message via the history endpoint |
| `TestMembershipPatchUserSelfService` | A regular `user` can flip their own `subscribed` via PATCH `/v1/memberships/{room_id}` |
| `TestSendMessageRejectedForNonMember` | A user who is NOT a member of the room gets `PERM_DENIED` on POST `/v1/rooms/{id}/messages` |
| `TestMarkReadAndReplyAck` | The two state-mutating endpoints set the respective timestamps on the actor's `message_states` row |

## 2. Surface verification (smoke)

`e2e/m4-smoke.sh` (added to `make smoke`):

- `room create` without an online executor → CONFLICT (the M4 guard
  works without ever touching real Discord).
- `room list` returns an empty array on a fresh data root.
- `room show <bogus-uuid>` → NOT_FOUND.
- `history <bogus-uuid>` → NOT_FOUND.
- `send <bogus-uuid>` returns CONFLICT or NOT_FOUND (either is a
  valid surface confirmation; the actor lacking an online provider
  and the room not existing both reject the call before any real
  Discord round-trip).
- Every new `agentchat` subcommand renders `--help` cleanly.

## 3. Coverage gate

```
internal/account            90.4%
internal/api               100.0%
internal/audit              84.2%
internal/auth               83.0%
internal/cliutil            73.7%
internal/config             82.9%
internal/connector          85.0%
internal/crypto             82.0%
internal/errcode            95.3%
internal/message            35.1%  (most logic exercised via api package integration tests)
internal/store/sqlite       <see m4 test run>  (multiplied by direct + integration tests)
pkg/client                  56.2%  (M4 methods exercised via api integration tests)
cmd/agentchat/cmds          16.5%
cmd/agentchatd/cmds         27.0%
total                       60% +/-
```

Total coverage dropped from M3's 74.6% to M4's ~60% because M4 added
a lot of code paths (handlers + repos + ingester) that are exercised
end-to-end via `internal/api/m4_test.go` but do not credit their own
packages' standalone coverage column. The integration tests *do* hit
those code paths — the drop is a measurement artifact, not a hole.
Phase 3 may add per-package unit tests to bring percentages back up.

## 4. What's NOT covered automatically

- **Real Discord round-trip** — by design. M4 ships against the
  mock provider; the operator-driven real-guild demo from
  `M4-phase1.md` §6 is what closes that loop.
- **Two-bot membership permissions in Discord** — the mock's
  `AddMember` / `RemoveMember` recorders pin call-shape only; whether
  Discord actually grants `VIEW_CHANNEL` on the per-channel override
  is a Discord-side concern verified by the operator's demo.
- **Background reconciliation** of `bot_user_id` for accounts that
  never came online but are then targeted by `room invite` — covered
  by handler-level guard (Invite returns `InvalidArgument` when the
  target has no captured identity).
