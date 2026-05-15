# M7 — Phase 1 Report (Implementation)

> Companion to `M7-phase2.md` (testing). Phase 1+2 executed as one
> continuous block per `05-engineering-workflow.md`.
>
> Per the user-driven workflow: this milestone stops here for the
> user's Phase 3 audit (which a developer-spawned review agent
> cannot substitute for — that's a *self-audit*).

**Date:** 2026-05-14
**Milestone scope:** `04-roadmap.md` §8 — daemon-side attachment
download + index + outbound upload with the Discord 25 MB cap.

## 1. Goal recap

Two halves:

- **Outbound** — `agentchat send <room> --attach <file>` reads a
  local file, the daemon uploads it via Provider, and the resulting
  Discord attachment metadata (filename, size, CDN URL) is indexed.
  Each file > 25 MB returns `ATTACHMENT_TOO_LARGE` (HTTP 413, exit
  22). Multi-file payloads are bounded per-file, not aggregate
  (M7-P3-002 fix; the initial implementation summed sizes — the
  correct boundary per `02-requirements-final.md` §3.4 is per file).
- **Inbound** — gateway messages carrying attachments produce
  `attachments` rows with `downloaded_at = NULL`; the background
  downloader fetches each file into
  `<data-root>/attachments/<message-id>/<attachment-id>/<filename>`
  and commits `local_path` + `sha256` + `downloaded_at`.

`history` renders each message with an `[ATTACHMENT]` line per
indexed file so agents/humans can `xdg-open` the local path.

## 2. Files added / modified

### New SQL migration

| File | Purpose |
|------|---------|
| `internal/store/sqlite/migrations/0004_m7_attachments.up.sql` | `attachments(id, message_id, filename, size, mime, local_path, discord_url, downloaded_at, sha256, created_at)` + per-message + pending-download indices |

### New / extended packages

| Package | Change |
|---------|--------|
| `internal/store/types.go` | New `Attachment` type |
| `internal/store/store.go` | New `AttachmentRepo` interface + `Bundle.Attachments` field |
| `internal/store/sqlite/attachment_repo.go` (new) | `attachmentRepo` over SQLite (Create / Get / ListByMessage / ListByMessages / ListPendingDownloads / MarkDownloaded) |
| `internal/store/sqlite/db.go` | `Bundle()` + `WithTx` wire the new repo |
| `internal/bot/types.go` | `Message.Attachments []MsgAttachURL`; new `UploadFile` struct; `SendOptions.Attachments []UploadFile` |
| `internal/bot/discord/discord.go` | `SendMessage` fast-path/file-path split; `ChannelMessageSendComplex` for uploads; `discordToMessage` carries `Attachments` |
| `internal/attachment/downloader.go` (new) | Long-running poller: every 2 s, `ListPendingDownloads` → HTTP GET each → SHA-256 → `MarkDownloaded`. Per-fetch context with 60 s timeout. 50 MiB max size cap (safety net above Discord's own 25 MB). |
| `internal/message/ingester.go` | inside `ingestNew` WithTx: after `Messages.CreateIgnoreConflict`, insert one `attachments` row per `e.Message.Attachments[i]` with empty local_path + downloaded_at = NULL; the downloader picks them up |
| `internal/api/v1/types.go` | `SendMessageRequest.Attachments []SendAttachmentRequest`; new `SendAttachmentRequest{Path, Filename, MIME}`; `MessageResponse.Attachments`; new `AttachmentResponse` |
| `internal/api/v1/helpers.go` | `AttachmentToResponse` mapper |
| `internal/api/v1/messages.go` | `SendMessage` accepts attachments; authz runs before attachment stat / size validation (M7-P3-001), then per-file size guard runs before Provider acquisition; persists `attachments` rows with `local_path = source` and `downloaded_at = now` (outbound files are already on disk); audit payload gets `attachments: <count>`. `ListMessages` batch-loads attachments via `ListByMessages` to avoid N+1 |
| `internal/errcode/errcode.go` | New `AttachmentTooLarge` code |
| `internal/errcode/exitcode.go` | Maps `AttachmentTooLarge` to exit 22 (same family as `InvalidArgument`) |
| `internal/errcode/http.go` | Maps `AttachmentTooLarge` to HTTP 413 (Payload Too Large) |
| `pkg/client/client.go` | `SendMessageOptions.Attachments []SendAttachment`; new `SendAttachment` struct; client resolves relative paths to absolute before posting (the daemon may run in a different CWD) |
| `cmd/agentchat/cmds/send.go` | `--attach` (repeatable) flag; attachment-only message allowed |
| `cmd/agentchat/cmds/history.go` | renders `[ATTACHMENT] msg=<id> name=<f> size=<n> mime=<m> -> <local_path|"(pending download)">` under each row |
| `cmd/agentchatd/cmds/serve.go` | constructs the `attachment.Downloader`, calls `Start(ctx)` + defers `Shutdown`; ensures `<data-root>/attachments/` exists (0o700) at boot |
| `e2e/m7-smoke.sh` (new) | Mock-driven CLI + API surface smoke |
| `Makefile` | smoke target gains `./e2e/m7-smoke.sh` |
| `README.md` | status: "M7 Phase 1+2 complete, awaiting Phase 3" |

## 3. Key design decisions

### 3.1 Two-phase attachment life cycle

Outbound (send-path) attachments land with `local_path` = the source
path the caller passed and `downloaded_at = now`. They're already on
the daemon's filesystem — no fetch needed. The agentchat-side row
exists so `history` and downstream agents can find the file
deterministically by querying the index instead of guessing where
the upload originated.

Inbound attachments land with `local_path = ''`, `downloaded_at =
NULL`, and the downloader fills them in as the bytes arrive. Until
the row commits a non-NULL `downloaded_at`, the CLI renders
`(pending download)` so agents know to poll once more before
consuming the file.

**`sha256` is asymmetric on purpose.** Outbound rows skip the hash
(the caller already had the file locally and can hash it themselves
if they want a content fingerprint). Inbound rows compute SHA-256
during the download so a later integrity check can detect a CDN
swap or local-disk corruption. The column is empty on outbound and
non-empty on downloaded inbound; consumers should treat empty as
"agentchat doesn't vouch for this hash" rather than as "the bytes
aren't on disk".

### 3.2 Downloader: polling, not eventing

`internal/attachment/downloader.go` polls `ListPendingDownloads` on
a 2-second tick. Reasons over an event channel from the ingester:

- The ingester writes the placeholder row inside its WithTx and the
  downloader is a separate process-wide goroutine — wiring a
  channel would mean either coupling the ingester's tx commit to a
  channel send (correctness risk) or running the channel send
  outside the tx (race-with-rollback).
- Polling cost is a single SELECT per cycle against the
  `idx_attachments_pending` partial index, which is negligible at
  our scale.
- Discord CDN URLs are time-limited but multi-minute, well within
  a 2 s poll.

A future milestone wanting tighter latency can add a notification
channel without changing the poll path.

### 3.3 Size limit enforced server-side, AFTER authorization (M7-P3-001 fix)

`SendMessage` runs the attachment stat + size guard AFTER the
WithTx that resolves room + member-or-admin authorization, and
BEFORE acquiring the Provider. Reasons (revised from the original
phase1 §3.3 which had the guard run first):

- **Authorization first** — letting an unauthorized caller `os.Stat`
  daemon-local files leaks filesystem state (path existence + size
  oracle via HTTP 400 vs 413). The audit caught this as M7-P3-001.
  Room + role + membership now gate first.
- **Pre-Provider second** — the size guard still runs before bot
  acquisition so an offline bot doesn't mask a bad attachment with
  a confusing `CONFLICT: no live Discord provider`.
- **Per-file, not aggregate** — Discord's per-message-attachment
  cap is `~25 MB / 文件`; checking the aggregate would reject a
  legitimate 2×14 MB message. See M7-P3-002 fix in
  `M7-phase3.md` §Resolution.

### 3.4 Outbound `discord_url` may lag behind the response

When the Provider's `SendMessage` returns, Discord has already
assigned CDN URLs for the uploaded files. We map
`sent.Attachments[i]` onto our `attachments` rows by index, so the
URLs land in the same WithTx as the message row. If the index match
ever goes off (e.g. Discord reorders, or upload partially succeeds)
the row keeps its filename + size + local_path; `discord_url` would
be empty but the file is still locally available. Future hardening:
match by filename hash rather than position.

### 3.5 Ingester bypass for the @all mirror still applies here

The M6 announcement-mirror posted a Discord-side message that the
ingester's own-bot filter drops, so `agentchat history` doesn't
show it. M7 outbound attachments land via the regular SendMessage
path (which inserts the messages row directly), so `history` does
show them. The M6 mirror is the exception, not the rule.

## 4. Auth + size matrix

| Endpoint | Rule |
|----------|------|
| `POST /v1/rooms/{id}/messages` with `attachments: [...]` | Same as regular send (member + admin); plus M7 size check |
| `GET /v1/rooms/{id}/messages` | Same as M4; response now hydrates `attachments[]` |
| Downloader (no HTTP endpoint) | Internal goroutine; no auth surface |
| Any single file > 25 MB | `ATTACHMENT_TOO_LARGE` → HTTP 413, exit 22 (per-file, not aggregate — M7-P3-002 fix) |
| Per-file inbound > 50 MiB cap | Downloader skips silently (row keeps `downloaded_at = NULL`, retried next cycle); log line at WARN |

## 5. Out of scope (deferred)

- **Resumable downloads.** The downloader fully re-fetches on each
  cycle; a partial file is overwritten by the next attempt. Acceptable
  given Discord CDN limits and the per-fetch 60 s ceiling.
- **Per-room download budget / rate-limit.** A burst of large
  messages can stack pending rows; the cycle processes 50 at a time
  and retries failures on the next tick. No quota or back-pressure
  signal back to the bot.
- **Local cache eviction.** `local_path` stays forever (until the
  message row is deleted via FK cascade). M8 can add an LRU /
  size-cap eviction if disk pressure becomes a concern.
- **Image / video thumbnailing.** Out of scope; the CLI renders the
  raw path.
- **MIME sniffing.** Inbound rows take whatever `ContentType` the
  Discord gateway provided. Outbound rows use the caller's `mime`
  field or empty; we don't sniff bytes.

## 6. Known UX trade-offs

- The downloader prefix-keys files under `<message-id>/<attachment-id>/`.
  Two same-name files in one message produce different dirs, which
  is correct but visually noisy for the common single-file case.
- `safeFilename` rewrites `.` / `..` / slashes to an `att-<id>`
  fallback. If a gateway message contained a maliciously-named file
  with a path separator, the local cache stays sandboxed.
