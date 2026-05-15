# M7 — Phase 2 Report (Testing)

> Companion to `M7-phase1.md`. The dev-side gate stays light per
> `feedback_iterative_tests_first.md`; full `make test-race` / `cover`
> / `smoke` are run at milestone close, not iteratively.

**Date:** 2026-05-14
**Status:** Phase 1+2 ready for user Phase 3.

## 1. Targeted Go tests added

### `internal/api/m7_test.go` (new, 4 tests)

| Test | What it asserts |
|------|-----------------|
| `TestSendMessageWithAttachmentPersisting Row` | Outbound `--attach` writes an `attachments` row with `local_path = source` and `downloaded_at != nil`; the response hydrates the attachment metadata. |
| `TestSendMessageAttachmentTooLargeReturns413` | A 26 MB upload returns HTTP 413 with `code: ATTACHMENT_TOO_LARGE`. |
| `TestSendMessageAttachmentMissingPathReturns400` | A nonexistent path returns 400 with `code: INVALID_ARGUMENT` and a `stat attachment` message hint. |
| `TestListMessagesHydratesAttachments` | `GET /v1/rooms/{id}/messages` returns each message with its `attachments[]` populated. |

### `internal/attachment/downloader_test.go` (new, 2 tests)

| Test | What it asserts |
|------|-----------------|
| `TestDownloaderFetchesPendingRow` | One pending row pointing at a `httptest` server gets downloaded; `local_path` is `<cacheRoot>/<message-id>/<attachment-id>/<filename>`; bytes on disk match the response; SHA-256 matches the bytes. |
| `TestDownloaderSkipsOversizeRow` | A row whose `Size` exceeds `MaxBytes` is NOT marked downloaded (stays pending; the downloader logs WARN but doesn't crash). |

### Pre-existing tests still passing

`internal/store/sqlite` (incl. M2 / M4 / M5 / M6 audit fixtures),
`internal/state`, `internal/api` (incl. M4 / M5 / M6 audit fixtures
and the M6 phase 3 audit tests), `internal/message`,
`internal/audit`, `internal/auth`, `pkg/client` — all green after
M7 lands.

## 2. End-to-end smoke

### `e2e/m7-smoke.sh` (new)

Mock-driven (matches M3 / M4 / M5 / M6). Real-Discord verification
is the operator's job and is printed at the end of the script.

Smoke checks:

1. Daemon boots cleanly with M7 wiring.
2. `<data-root>/attachments/` directory is created at boot (by
   `serve.go` before `Downloader.Start`).
3. `agentchat send --help` exposes the `--attach` flag.
4. `POST /v1/rooms/{unknown}/messages` with an attachment path
   returns HTTP 404 before stat'ing the file, proving room authz runs
   before attachment path / size validation (M7-P3-001).
5. Authorized Go integration tests cover `ATTACHMENT_TOO_LARGE` and
   missing-path validation after authz has succeeded.

Operator playbook for real Discord verification:

```
1. Online a bot; create a room; invite a subscribed viewer.
2. agentchat send <room> --attach /tmp/screenshot.png 'look'
   → bot uploads the file; Discord client shows the image inline
3. As the viewer:
   agentchat history <room>
   → row has [ATTACHMENT] entry; local_path points under
     <data-root>/attachments/
4. xdg-open <local_path> opens the cached copy.
```

## 3. Build / static checks

- `go build ./...` — clean
- `go vet ./...` — clean
- `gofmt -l` — clean

## 4. Coverage delta

Not measured during Phase 1+2 per workflow rules. Expected modest
bumps in `internal/store/sqlite` (new attachment_repo), `internal/api`
(M7 SendMessage path + hydrate helpers), and `internal/attachment`
(new package). M5/M6 packages unchanged.

## 5. Open questions for Phase 3

The implementation made several judgement calls the auditor may
want to revisit:

- **Pre-Provider size check ordering** (§3.3 in phase1) — the test
  hits 413 before the bot has to be online. Auditor may prefer
  acquiring the Provider first to gate "can this caller even send
  here" before evaluating payload.
- **Downloader cadence** — fixed at 2 s; no jitter, no
  back-pressure. Cheap, simple, but a future workload with very
  bursty inbound attachments could see chunky latency.
- **`safeFilename` strips path separators silently** — a maliciously
  named inbound attachment becomes `att-<id>` without surfacing the
  original name anywhere. Audit may prefer logging + tagging the
  row.
- **Outbound `discord_url` matched by position** (§3.4) — fragile
  if Discord reorders. Audit may want filename-keyed matching.
- **No HEAD pre-check for inbound size** — the roadmap mentions
  "big files first do a HEAD"; we skipped this because the
  Discord-CDN serves URLs that lie about Content-Length sometimes
  and the LimitReader stream-cap is the authoritative defence.
