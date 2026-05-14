# M4 — Phase 1 Report (Implementation)

> Companion to `M4-phase2.md` (testing). Phase 1+2 executed as one
> continuous block per `05-engineering-workflow.md`.
>
> Per the M-workflow: this milestone stops here so the operator can
> run the real-Discord verification. **Phase 3 audit happens after
> that verification.**

**Date:** 2026-05-14
**Milestone scope:** `04-roadmap.md` §5 — rooms (= 1:1 Discord
channels) + memberships (with the separate subscribed flag) +
messages with per-account read/reply state.

## 1. Goal recap

Bring the core IM data plane online. After M4 the daemon can:

1. Create / list / show / rename / archive / delete rooms — each
   backed by a real Discord channel inside the configured guild.
2. Manage memberships (admin invite/kick + user self subscribe /
   unsubscribe) and propagate channel permissions to Discord.
3. Send messages via the actor's bot, persist them in SQLite, and
   fan out per-subscriber `message_states` (initial read_at = nil).
4. Ingest inbound `EventMessageNew` events in the background, dedupe
   against outbound echoes via the `discord_msg_id` UNIQUE index,
   and persist + fan out states identically.
5. Mark a message read or reply-ack on behalf of the actor.
6. Page back through history (`before=<id>&limit=<n>`).

## 2. Files added / modified

### New packages

| Package | Purpose |
|---------|---------|
| `internal/message/` | Inbound-message ingester (`ingester.go`): per-account drain goroutine over the Connector subscription that runs `bundler.WithTx` to insert the message + per-subscriber `message_states` row |

### Extended packages

| Package | Change |
|---------|--------|
| `internal/store/types.go` | New types: `Room`, `Membership`, `Message`, `MessageState`, `Reaction`, `MessageFilter`, `MessagePriority`; `Account` grows `BotUserID` |
| `internal/store/store.go` | New repo interfaces: `RoomRepo`, `MembershipRepo`, `MessageRepo`, `MessageStateRepo`; `Bundle` grows four fields |
| `internal/store/sqlite/` | Migration `0002_m4_rooms_messages.up.sql`; new files `room_repo.go`, `membership_repo.go`, `message_repo.go`, `message_state_repo.go`; existing `account_repo.go` reads/writes the new `bot_user_id` column; `db.go` Bundle() and WithTx wire the four new repos |
| `internal/config/config.go` | New `DiscordConfig` block (`[discord] guild_id`); env override `AGENTCHAT_DISCORD_GUILD_ID` |
| `internal/audit/audit.go` | New action verbs: `room.create / rename / archive / delete / member.add / member.remove`, `membership.subscribe / unsubscribe`, `message.send / read / reply_ack` |
| `internal/api/v1/` | New files `rooms.go`, `messages.go`; new DTOs in `types.go`; new converters in `helpers.go`; `discord.go` `OnlineAccount` captures `bot_user_id` from `Provider.Identity()` and calls `Ingester.AttachAccount`; `OfflineAccount` calls `Ingester.DetachAccount` before `Connector.Disconnect` |
| `internal/api/server.go` | `Deps.Ingester` field; M4 routes wired into the auth-only and admin-only groups |
| `cmd/agentchatd/cmds/serve.go` | Constructs `*message.Ingester` and threads it into `api.Deps`; threads `cfg.Discord.GuildID` into the Discord factory |
| `cmd/agentchat/cmds/` | New files `room.go`, `send.go`, `history.go`, `message.go` |
| `pkg/client/client.go` | New methods: `CreateRoom`, `ListRooms`, `GetRoom`, `RenameRoom`, `ArchiveRoom`, `DeleteRoom`, `InviteMember`, `KickMember`, `ListMembers`, `SetSubscribed`, `SendMessage`, `ListMessages`, `MarkMessageRead`, `ReplyAckMessage` + `SendMessageOptions`, `ListRoomsOptions`, `ListMessagesOptions` |
| `Makefile` | `COVER_PKGS` adds `./internal/message`; `smoke` target runs `m4-smoke.sh` |
| `e2e/m4-smoke.sh` | Mock-driven M4 surface verification |

## 3. Dependencies added

None. M4 builds entirely on packages M1–M3 already pulled in
(`google/uuid`, `chi`, `modernc.org/sqlite`, etc.).

## 4. Architecture decisions made in flight

1. **Rooms map 1:1 to Discord channels.** A `room` row is created
   in lockstep with `bot.CreateChannel`; the `discord_channel_id`
   column is UNIQUE so a duplicate Discord channel can never produce
   two rooms.
2. **GuildID is system-wide config.** Per requirements §3.2
   ("一个 guild"), the daemon takes a single `discord.guild_id`. All
   accounts' bots are expected to be members of that one guild.
   Setting is via `[discord] guild_id` in `config.toml`, with
   `AGENTCHAT_DISCORD_GUILD_ID` as env override.
3. **The actor's bot is the executor.** Room CRUD, invite, and kick
   all use the **caller's** bot to issue the Discord-side operation
   (CreateChannel, ChannelPermissionSet, ChannelPermissionDelete).
   The caller's account must be online; we reject with `Conflict`
   otherwise — symmetric with the M3 send/online guarantees. This
   keeps M4 simple; multi-bot orchestration is a later concern.
4. **`Account.BotUserID` is the inbound-author map.** On the first
   successful `online` we capture `provider.Identity().UserID` and
   persist it. The ingester resolves inbound `author_id` →
   `account_id` by scanning accounts for a matching `BotUserID`.
   Empty (NULL) means "external Discord user" — those messages still
   land in `messages` for history but with `author_account_id` NULL.
5. **Send-vs-ingest dedupe via `discord_msg_id` UNIQUE + INSERT OR
   IGNORE.** When `POST /v1/rooms/{id}/messages` runs, the send-path
   persists authoritatively. The gateway's echo arrives later through
   the ingester; `MessageRepo.CreateIgnoreConflict` short-circuits
   on the UNIQUE violation and returns the existing row id, so the
   ingester skips the duplicate state fan-out. **Either path can
   win the race** — both produce the same outcome.
6. **Fan-out happens at write time.** Per requirements §4 and §5.1,
   "收 vs 订阅" is decoupled. We insert one `message_states` row per
   current subscriber at the moment the message is persisted. The
   M5 state aggregator will read this table; we don't compute
   per-account state at read time.
7. **Author is "read" automatically.** A user who sends a message is
   not "unread" for that message, regardless of their subscription
   state. The send and ingest paths both set `read_at = now` for
   the author's state row.
8. **Membership upsert preserves `joined_at`.** Toggling subscribed
   does NOT reset the join timestamp (verified by
   `TestMembershipUpsertPreservesJoinedAt`). This matters for the
   M5 state UI's "new room" detection and for audit forensics.
9. **`reactions` table is created but unused.** Requirements §3.3
   includes reactions; M4 ships the schema so M5+ migrations don't
   reshuffle, but no API surfaces it yet.
10. **Reply targeting is agentchat-id only.** `SendMessage`'s
    `reply_to_id` points to an agentchat message UUID; the handler
    resolves it to the parent's `discord_msg_id` for the actual
    Discord `MessageReference`. Inbound Discord replies do NOT
    populate `reply_to_msg_id` on ingest — that lookup is deferred
    to a later milestone.
11. **Ingester is per-account, lifecycle-bound.** `AttachAccount` is
    called by the `OnlineAccount` handler after the tx commits;
    `DetachAccount` is called by `OfflineAccount` before
    `Connector.Disconnect`. The drain goroutine exits when the
    Connector subscription channel closes — so even if `Detach` is
    skipped (e.g. daemon-shutdown path), no goroutine leaks.

## 5. Intentionally deferred (M5+)

- **`EventMemberJoined` / `EventMemberLeft` normalization** — Roadmap
  §5 lists them, but Discord does NOT emit a clean
  "member joined / left channel" gateway event; the closest signals
  are `GUILD_MEMBER_ADD` / `GUILD_MEMBER_REMOVE` (guild-level, not
  channel-level) and `CHANNEL_UPDATE` (which carries the full new
  permission set and would need diffing against the old to derive
  member changes). The Membership model in agentchat is *channel-
  permission-scoped*, so neither signal maps 1:1. M4 ships without
  these events; agentchat membership is the source of truth and is
  only mutated via the `room invite` / `room kick` / `PATCH
  /memberships/...` API paths. M5 (state aggregator) will reconcile
  on demand, and if Discord-side drift becomes a concern a future
  audit-driven reconciliation job is the right place for it. This is
  finding **M4-P3-006** deferred with explicit acknowledgement, not
  silently dropped.
- **State aggregator (`internal/state`)** — M5's job. M4 only writes
  the rows; M5 reads them into the agent-facing state UI.
- **Mentions parsing (`@me`)** — M5+. The schema does not track
  mention sets; M5 will derive @-mention status from `content` plus
  the room membership.
- **Attachments** — M7's job. The `messages` schema has no
  attachment columns; M7 adds the `attachments` table.
- **Announcements (room / @all / system)** — M6's job. M4 leaves
  `priority = 'system'` available so M6 can use it without another
  migration.
- **Bot rename forwarding to Discord** — `PATCH /v1/rooms/{id}`
  updates our row's name only; the Discord channel keeps its old
  name. (Discord's `ChannelEdit` would do it; deferred until M5 to
  cut M4 scope.)

## 6. Demo (real-Discord flow, manual)

```bash
# Pre-req: a Discord application + bot, MessageContent + GuildMembers
# intents enabled, bot added to your guild with Manage Channels
# permission. Note the guild_id and the admin's bot token.

make build
mkdir -p /tmp/m4demo
cat >/tmp/m4demo/config.toml <<EOF
[discord]
guild_id = "<your-guild-id>"
EOF

./bin/agentchatd serve --data-root /tmp/m4demo
# Note the printed AGENTCHAT_TOKEN.

# In another shell:
export AGENTCHAT_TOKEN=<paste>
export AGENTCHAT_HOME=/tmp/m4demo

# The bootstrap root is admin. Give it a Discord bot and online it.
./bin/agentchat admin account set-discord <root-id> --bot-token <admin-bot-token>
./bin/agentchat admin account online <root-id>

# Create a room (this calls bot.CreateChannel against your guild).
./bin/agentchat room create --name ops
ROOM=$(./bin/agentchat room list --json | jq -r '.[0].id')

# Send a message and read history.
./bin/agentchat send "$ROOM" "hello from M4"
./bin/agentchat history "$ROOM"

# (Optional) Invite a user agent — needs that user's bot online once
# to capture its bot_user_id.
# ./bin/agentchat admin account create --name agent1 --role user
# ./bin/agentchat admin account set-discord <agent1-id> --bot-token <agent1-bot-token>
# ./bin/agentchat admin account online <agent1-id>
# ./bin/agentchat room invite "$ROOM" <agent1-id> --subscribe
```

## 7. Layout snapshot (M4 additions)

```
internal/store/                        types.go store.go                (existing pkg, extended)
internal/store/sqlite/                 room_repo.go membership_repo.go
                                       message_repo.go message_state_repo.go
                                       sqlite_m4_test.go
                                       migrations/0002_m4_rooms_messages.up.sql
internal/message/                      ingester.go ingester_test.go testutil_test.go
internal/api/v1/                       rooms.go messages.go             (existing pkg, extended)
internal/api/                          server.go m4_test.go             (existing pkg, extended)
cmd/agentchat/cmds/                    room.go send.go history.go message.go
cmd/agentchatd/cmds/                   serve.go                          (Ingester wiring)
pkg/client/                            client.go                         (existing pkg, extended)
e2e/                                   m4-smoke.sh
docs/milestones/                       M4-phase1.md M4-phase2.md
```
