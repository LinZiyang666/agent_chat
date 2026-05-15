# M6 — Phase 1 Report (Implementation)

> Companion to `M6-phase2.md` (testing). Phase 1+2 executed as one
> continuous block per `05-engineering-workflow.md`.
>
> Per the user-driven workflow: this milestone stops here for the
> user's Phase 3 audit (which a developer-spawned review agent
> cannot substitute for — that's a *self-audit*).

**Date:** 2026-05-14
**Milestone scope:** `04-roadmap.md` §7 — three announcement surfaces
(room announcements / @all mentions / system announcements) + their
M5 state-aggregator integration.

## 1. Goal recap

Three independent announcement mechanisms (requirements §6 / roadmap §7):

1. **Room announcements** — group-scoped, replaces prior version on
   every post, forces unread on every current member of the room
   regardless of subscription state.
2. **@all message flag** — `mention_all` column on `messages`: any
   member of the room sees the message in their mentions feed,
   independent of the literal `<@bot_user_id>` content match.
3. **System announcements** — admin-only, global; every account is
   implicitly unread until they ACK.

All three feed the M5 Snapshot via two new dimensions
(`announcements`, `system_announcements`) and two new Totals counters.

## 2. Files added / modified

### New SQL migration

| File | Purpose |
|------|---------|
| `internal/store/sqlite/migrations/0003_m6_announcements.up.sql` | Adds `announcements`, `announcement_reads`, `system_announcements`, `system_announcement_reads`; adds `messages.mention_all` (NOT NULL DEFAULT 0, CHECK 0/1). |

### New repos

| File | Provides |
|------|----------|
| `internal/store/sqlite/announcement_repo.go` | `announcementRepo` (Create / Get / Latest / NextVersion); `announcementReadRepo` (Upsert / IsRead / CountUnreadForAccount / ListUnreadForAccount) |
| `internal/store/sqlite/system_announcement_repo.go` | `systemAnnouncementRepo` (Create / Get / List); `systemAnnouncementReadRepo` (Upsert / IsRead / CountUnreadForAccount / ListUnreadForAccount) |

### Extended packages

| Package | Change |
|---------|--------|
| `internal/store/types.go` | New types `Announcement`, `AnnouncementRead`, `SystemAnnouncement`, `SystemAnnouncementRead`. `Message.MentionAll bool` added. |
| `internal/store/store.go` | Four new repo interfaces (`AnnouncementRepo`, `AnnouncementReadRepo`, `SystemAnnouncementRepo`, `SystemAnnouncementReadRepo`); `Bundle` gained four fields. `SendMetadata.MentionAll bool` added. |
| `internal/store/sqlite/db.go` | `Bundle()` + `WithTx` wire the four new repos. |
| `internal/store/sqlite/message_repo.go` | INSERT / INSERT-OR-IGNORE / ApplySendMetadata / scanMessageRow / LatestPerRoomForMember all carry `mention_all`. |
| `internal/store/sqlite/message_state_repo.go` | `CountMentionsForSubscribed` + `ListMentionsForSubscribed` predicate widened: `(? <> '' AND content LIKE '%<@bot>%') OR mention_all = 1`. Empty `botUserID` still matches mention_all rows. Existing SELECT col lists carry `mention_all`. |
| `internal/state/types.go` | `Snapshot` gained `Announcements []AnnouncementEntry` and `SystemAnnouncements []SystemAnnouncementEntry`. `Totals` gained `Announcements int` and `SystemAnnouncements int`. New types `AnnouncementEntry`, `SystemAnnouncementEntry`. |
| `internal/state/aggregator.go` | Build assembles the two new dimensions + their counts. New caps `announcementsLimit=20`, `systemAnnouncementsLimit=20`. |
| `internal/api/v1/types.go` | `SendMessageRequest.MentionAll` + `MessageResponse.MentionAll`. New DTOs: `CreateAnnouncementRequest`, `AnnouncementResponse`, `AnnouncementReadResponse`, `CreateSystemAnnouncementRequest`, `SystemAnnouncementResponse`, `SystemAnnouncementListResponse`, `SystemAnnouncementReadResponse`. |
| `internal/api/v1/helpers.go` | `MessageToResponse` carries `MentionAll`. New helpers `AnnouncementToResponse`, `SystemAnnouncementToResponse`. |
| `internal/api/v1/messages.go` | `SendMessage` reads `req.MentionAll`, threads it into `Message` + `SendMetadata`, includes it in the `message.send` audit payload. |
| `internal/api/v1/announcements.go` (new) | Five handlers: `CreateAnnouncement`, `GetAnnouncement`, `MarkAnnouncementRead`, `CreateSystemAnnouncement`, `ListSystemAnnouncements`, `MarkSystemAnnouncementRead`. |
| `internal/api/server.go` | Routes wired: `POST/GET /v1/rooms/{id}/announcement`, `POST /v1/announcements/{id}/read`, `POST /v1/system/announcements` (admin gate), `GET /v1/system/announcements`, `POST /v1/system/announcements/{id}/read`. |
| `internal/audit/audit.go` | Four new `Action` constants: `announcement.create`, `announcement.read`, `system_announcement.create`, `system_announcement.read`. |
| `pkg/client/client.go` | `SendMessageOptions.MentionAll`; six new methods: `CreateAnnouncement`, `GetAnnouncement`, `AckAnnouncement`, `CreateSystemAnnouncement`, `ListSystemAnnouncements`, `AckSystemAnnouncement`. |
| `cmd/agentchat/cmds/announce.go` (new) | CLI verbs: `room announce`, `room announce-show`, `ack-announcement`, `system-announcements`, `ack-system`, `admin system-announce`. |
| `cmd/agentchat/cmds/send.go` | Adds `--all` flag wired through `SendMessageOptions.MentionAll`. |

## 3. Key design decisions

### 3.1 Versioning model for room announcements

Each `room announce` invocation inserts a fresh row with
`version = max(version)+1` in that room. The GET endpoint returns
**only the highest-version row**; older versions are kept in the
table for history / audit but never surfaced. `CountUnreadForAccount`
and `ListUnreadForAccount` count only the latest version per room.

Implication: a member who acked v1 and is then exposed to v2 is once
again unread on v2. The ack row is keyed by `(announcement_id,
account_id)`, so the old ack row stays attached to v1 — there is no
ambiguity about "did they ack the current content?".

### 3.2 Membership scope of the unread query

`CountUnreadForAccount` JOINs `memberships` with no filter on
`subscribed` or `archived`. Announcements are mandatory-read across
**all three membership facets**:

- Both `subscribed=true` (订阅) and `subscribed=false` (旁观) members
  see the unread badge — unsubscribing does NOT suppress
  announcements (M6-S3, self-audit).
- Archived rooms still propagate their unread announcement count.
  This is deliberate: a freshly-archived room's outstanding
  mandatory-read items shouldn't silently vanish from the agent's
  state view. If the user wants to drop them, the cure is `kick`
  (membership delete), which cascades the ack row CASCADE.

### 3.3 `mention_all` semantics

The M5 mentions feed used to be exactly `content LIKE '%<@bot>%'`.
M6 widens this to `(<@bot> match) OR (mention_all = 1)`. An account
whose own send carries `mention_all=true` does NOT see their own
message in mentions (their own `message_state.read_at` is set to
`now()` by the send-path fan-out, and the mentions query filters on
`read_at IS NULL`).

`mention_all` is in `SendMetadata`, so the race-with-ingest path
(M4-P3-010) also persists the flag correctly: when the ingester wins
the discord_msg_id INSERT, `ApplySendMetadata` UPDATEs the row to
include `mention_all`.

### 3.4 State publish fan-out on system announcement create

`CreateSystemAnnouncement` calls `Accounts.List` and then
`bus.PublishMany(everyone)`. For our current scale (handful of
accounts) this is fine; if account count grows past a few hundred
this becomes a notable hot path. M6 does not optimize; flagged as a
known scale ceiling.

### 3.5 SQLite concurrency on `NextVersion+Create`

`announcementRepo.Create` requires the caller to pre-fetch
`NextVersion(roomID)`. The handler does both inside a single
`WithTx`, so any concurrent `room announce` against the same room
serializes on the single SQLite writer (`MaxOpenConns=1`). Even if
two requests bypass `WithTx`, the single-connection pool prevents
collision. Documented for future maintainers who might raise the
connection cap.

### 3.6 Auth matrix

| Endpoint | Required role |
|----------|---------------|
| `POST /v1/rooms/{id}/announcement` | Member of the room OR admin |
| `GET /v1/rooms/{id}/announcement` | Member of the room OR admin |
| `POST /v1/announcements/{id}/read` | Member of the room OR admin |
| `POST /v1/system/announcements` | **Admin only** (server-side `RequireAdmin` group) |
| `GET /v1/system/announcements` | Any authenticated |
| `POST /v1/system/announcements/{id}/read` | Any authenticated |
| `POST /v1/rooms/{id}/messages` with `mention_all=true` | Same as regular send (member + admin) |

## 4. State view interaction

The M5 Snapshot grows from 8 dimensions to **8 dimensions + 2 M6
extensions**. The extensions are separate fields rather than
overloading existing ones, because their UI behavior differs from
regular messages:

- `Snapshot.Announcements` — capped at 20, per-room latest unread
- `Snapshot.SystemAnnouncements` — capped at 20, global unread
- `Totals.Announcements` — unbounded count
- `Totals.SystemAnnouncements` — unbounded count

Watch frame: every announcement create / ack triggers a debounced
publish (room-scoped for create, single-account for ack); every
system-announcement create publishes to every account, every ack
publishes to one.

## 5. Out of scope (deferred)

- **`down.sql` for v3.** Project convention from M1: forward-only
  migrations. Restoring from snapshot is the rollback path.
- **Pagination on announcement history.** GET returns latest only;
  per-version browsing would need a `?version=` or
  `GET /v1/announcements/{id}` (not yet wired). Track if any caller
  wants it.
- **System announcement priority bands.** Currently they live in
  their own dimension; the priority field on `messages` stays
  message-only. A user-facing "system_announcement.priority" field
  is plausible but not requested.
- **Bulk ack.** No `POST /v1/announcements/read-all`. Each ack is
  one round-trip.

## 6. Self-audit findings

Captured as `M6-S1`..`M6-S9` in chat at milestone time; all minor /
design notes, no blockers. The three that translated into code-side
changes:

- **M6-S6**: added `TestAckAnnouncementIdempotent`,
  `TestGetAnnouncementNotFoundForEmptyRoom`,
  `TestAckAnnouncementUnknownIDReturns404` to fill coverage gaps the
  self-audit identified.
- **M6-S3**: archived-room unread propagation is now documented as
  intentional in §3.2.
- **M6-S5**: scale ceiling on system-announcement publish documented
  in §3.4.

Phase 3 will look for issues that this self-audit missed by
construction.
