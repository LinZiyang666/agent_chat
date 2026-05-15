# M5 — Phase 1 Report (Implementation)

> Companion to `M5-phase2.md` (testing). Phase 1+2 executed as one
> continuous block per `05-engineering-workflow.md`.
>
> Per the user-driven workflow: this milestone stops here for the
> user's Phase 3 audit (which a developer-spawned review agent
> cannot substitute for — that's a *self-audit*).

**Date:** 2026-05-14
**Milestone scope:** `04-roadmap.md` §6 — state aggregation engine +
`watch state` NDJSON stream covering the 8 dimensions of
requirements §5.2.

## 1. Goal recap

Surface the 8-dimension summary view agents subscribe to instead of
scanning history:

1. Totals (aggregate unread / mentions / pending acks / priority)
2. Per-room unread counts (subscribed rooms only)
3. @-me mentions list (unread, by content `<@bot_user_id>` match)
4. Pending ack list (`requires_ack=1` AND `replied_at IS NULL`)
5. Priority feed (urgent + system, unread)
6. New rooms (memberships joined within the last 24h)
7. Recently active rooms (subscribed, ordered by `MAX(messages.created_at)`)
8. Health bar (token + provider status + Discord reachability)

Transport: NDJSON over HTTP, debounced at 200 ms (D5 +
roadmap §6).

## 2. Files added / modified

### New packages

| Package | Purpose |
|---------|---------|
| `internal/state/` | `types.go` (Snapshot DTO + sub-types), `aggregator.go` (per-account snapshot build from `store.Bundle` + `ProviderStatusFn`), `bus.go` (atomic version + 200 ms debouncer + non-blocking fan-out + nil-safe Publish), with `aggregator_test.go` + `bus_test.go` |

### Extended packages

| Package | Change |
|---------|--------|
| `internal/store/store.go` | New methods on `MessageStateRepo` (`CountUnreadForSubscribed`, `UnreadCountByRoomForSubscribed`, `ListMentionsForSubscribed`, `ListPendingAcksForSubscribed`, `ListPriorityForSubscribed`) and `MessageRepo` (`LatestPerRoomForMember`). All scoped to subscribed memberships per requirements §5.2.1. |
| `internal/store/sqlite/` | Implementations of the six new repo methods (joined queries with `memberships.subscribed = 1`); `drainMessageRows` helper |
| `internal/api/v1/` | New file `state.go` (`GetState`, `WatchState` NDJSON streamer with `http.Flusher`); every M4 mutation handler (`SendMessage`, `MarkRead`, `ReplyAck`, `CreateRoom`, `UpdateRoom`, `ArchiveRoom`, `DeleteRoom`, `InviteMember`, `KickMember`, `UpdateMembership`) gained a `bus *state.Bus` parameter and calls `Publish` after the tx commits |
| `internal/message/ingester.go` | Constructor now takes a `*state.Bus`; `ingestNew` calls `bus.PublishMany` after the per-member state fan-out |
| `internal/api/server.go` | `Deps.StateBus` field; routes pass it through; new `/v1/state` + `/v1/state/watch` routes mounted on the auth-only group (member + admin both reachable) |
| `cmd/agentchatd/cmds/serve.go` | Constructs the Aggregator from `db.Bundle()` + `conn.Status`; constructs the Bus; defers `Bus.Shutdown`; threads it into `api.Deps` AND into the Ingester |
| `cmd/agentchat/cmds/` | New file `state.go` providing `agentchat state` (one-shot, JSON or human render) and `agentchat watch state` (NDJSON pass-through) |
| `pkg/client/client.go` | New `GetState` (returns `map[string]any` for forward-compat) + `WatchState` (returns `io.ReadCloser` for caller-managed NDJSON streaming) |
| `Makefile` | `COVER_PKGS` adds `./internal/state`; `smoke` runs `m5-smoke.sh` |
| `e2e/m5-smoke.sh` | Mock-driven surface verification: GET /v1/state returns valid snapshot shape, version monotonically increases, watch emits initial frame then stays silent on idle daemon |

## 3. Dependencies added

None. M5 reuses the M1–M4 stack.

## 4. Architecture decisions made in flight

1. **Subscription gates primary state, not message persistence.**
   The aggregator's six new repo queries all `JOIN memberships
   WHERE subscribed = 1`. Unsubscribed (旁观) rooms are visible in
   `agentchat history` (M4-P3-005 outcome) but DON'T contribute to
   the primary state UI counters — exactly matching requirements §4
   and §5.2.1.

2. **Bus version is daemon-global, not per-account.** A single
   `atomic.Int64` increments on every `BuildNow` call. Per-account
   ordering still works because each subscriber sees a strictly
   increasing version on their own channel. This is simpler than
   per-account counters and the values are not externally meaningful
   (clients use them only to detect "I missed an update").

3. **`Bus.Publish` is nil-safe.** Every mutation handler can pass
   `bus.Publish(...)` unconditionally; tests that don't wire a bus
   (M3 + M4 rigs) thread `nil` through `api.Deps.StateBus` and
   nothing panics. Same pattern as the M4 `*state.Bus`-nil branch
   in the ingester.

4. **Debounce window: 200 ms in production, 30 ms in tests.** The
   bus accepts a custom debounce via `NewBusWithDebounce` so the
   integration tests don't pay the full 200 ms per assertion. The
   smoke script uses the default 200 ms — verifying the user-facing
   value.

5. **Fan-out scope per mutation type.**
   - **`SendMessage`, `ArchiveRoom`, `UpdateRoom`, `DeleteRoom`,
     ingester `EventMessageNew`**: publish to every member of the
     affected room (state varies per member).
   - **`MarkRead`, `ReplyAck`, `UpdateMembership`**: publish to the
     actor only.
   - **`InviteMember`**: publish to the target (gained membership).
   - **`KickMember`**: publish to the target (lost membership).
   - **`CreateRoom`**: publish to the creator (only member at this
     point).

6. **`DeleteRoom` snapshots the member list *inside* the read tx**
   before the CASCADE clears the membership rows. The publish then
   reaches everyone whose state just lost a room.

7. **Empty `bot_user_id` means no mentions.** An agent that has
   never come online cannot participate in `@me` detection — the
   aggregator short-circuits with an empty mention list rather than
   issuing a "match anyone" `LIKE`.

8. **Idle streams stay byte-quiet.** No keepalive, no heartbeat —
   exactly per the roadmap §6 verification ("`socat - UNIX-CONNECT:...sock`
   sees nothing during the 10s idle window"). Connection liveness
   is the TCP/Unix-socket layer's job; the daemon does not paper
   over half-open sockets.

9. **Health bar M5 fields only.** `TokenOK` (always true at emit
   time since auth gates the request), `ProviderStatus` (mapped
   from `Connector.Status`), `DiscordReachable` (= status==online).
   `RecentErrors` reserved for M8 daemon-wide error aggregation;
   present in the schema, empty for now.

## 5. Intentionally deferred (M6+)

- **Mention-by-display-name.** M5 only matches the Discord raw
  mention token `<@bot_user_id>`. Plain "@alice" in the message
  text doesn't count. M6's announcements pass adds `@all` semantics;
  display-name `@`-resolution stays a later enhancement.
- **Announcement-driven "new rooms with required reads".**
  Requirements §5.2.1 dim 6 includes "新加入的群（带必读公告）".
  M5 surfaces the new-rooms entries but the "required announcement"
  attachment lands in M6 with the announcement subsystem.
- **Secondary state UI endpoint.** Requirements §5.2.2 says the
  旁观 view "isn't pushed; agent queries on demand". The aggregator
  already filters subscribed-only for the primary surface; a separate
  `GET /v1/state/secondary?room=<id>` would be a thin wrapper around
  existing read paths. Deferred until M7/M8 — no pressing demo need.
- **`RecentErrors` in the health bar.** M5 emits an empty list; M8
  wires a daemon-wide ring buffer.

## 6. Demo (real-Discord flow, manual)

```bash
# Pre-req: M4 demo state — admin bot online, a room with at least
# one subscribed user member.

# Terminal A (user agent):
export AGENTCHAT_TOKEN=<user-token>
./bin/agentchat watch state
# First frame lands immediately. Idle stays silent.

# Terminal B (admin):
export AGENTCHAT_TOKEN=<admin-token>
./bin/agentchat send <room-id> "hello state UI" --requires-ack --priority urgent

# Terminal A: a new NDJSON frame lands within ~200 ms with
#   totals.unread = 1
#   totals.pending_acks = 1
#   totals.priority = 1
#   rooms[…].unread = 1
#   pending_acks[…].id = <new message id>   (M5 originally emitted `message_id`; renamed in M6-P3-normalize)
#   priority[…] same

# Terminal A (still as user):
./bin/agentchat read <message-id>
./bin/agentchat reply-ack <message-id>
# Both produce two more frames; totals.unread / pending_acks drain
# back to zero.
```

## 7. Layout snapshot (M5 additions)

```
internal/state/                    types.go aggregator.go bus.go
                                   aggregator_test.go bus_test.go
internal/store/                    store.go                          (existing pkg, extended)
internal/store/sqlite/             message_repo.go                   (existing pkg, extended)
                                   message_state_repo.go             (existing pkg, extended)
internal/api/v1/                   state.go                          (new file)
                                   rooms.go messages.go              (existing pkg, +bus param)
internal/api/                      server.go m5_test.go              (existing pkg, extended)
internal/message/                  ingester.go                       (existing pkg, +bus param)
cmd/agentchat/cmds/                state.go                          (new file)
cmd/agentchatd/cmds/               serve.go                          (Bus wiring)
pkg/client/                        client.go m5_test.go              (existing pkg, extended)
e2e/                               m5-smoke.sh
docs/milestones/                   M5-phase1.md M5-phase2.md
```
