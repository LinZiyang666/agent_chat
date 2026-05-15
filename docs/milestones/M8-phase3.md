# M8 — Phase 3 Report (audit + fixes + verification)

> Companion to `M8-findings.md` (audit aggregation). M8 differs from
> M3-M7 in that there is no "implementation Phase 1+2 then audit" —
> the milestone itself **is** the project-wide polish pass, driven
> by parallel agent audits.

**Date:** 2026-05-15 (continued from 2026-05-14 M7 close)
**Scope:** project-wide code quality, security, bug, docs, CLI, build
audit + targeted fixes. Six audit agents ran in parallel; their full
reports live at `/tmp/m8-{security,codequality,test,docs,cli,build}-findings.md`.
The consolidated finding index is `M8-findings.md` in this directory.

## 1. Audit summary

| Agent | P0 | P1 | P2 |
|-------|---:|---:|---:|
| security | 0 | 2 | 17 |
| code-smell + bug | 3 | 18 | ~12 |
| tests | 3 | 7 | 8 |
| docs ↔ code drift | 0 | 15 | 8 |
| CLI / UX | 1 | 10 | 12 |
| build / ship | 0 | 8 | – |
| **total** | **7** | **60** | **~57** |

## 2. Fixes landed in M8

Citation-ID → fix:

### P0 — real bugs

- **M8-Q-P0-001** `helpers.go::DecodeJSON` EOF detection was dead code (`errors.Is(err, errors.New("EOF"))` always false). Replaced with `errors.Is(err, io.EOF)` as part of the S-P1-001 wrap.
- **M8-Q-P0-002** `connector.pump` tail wiped subs across generations: a fresh `Connect(accountID)` whose subscribers registered during a previous pump's drain window would see them all closed and the slice deleted. Fixed via per-instance `pumpDone chan struct{}` — `Disconnect`'s success path now blocks on the previous pump's cleanup before releasing the instance slot.
- **M8-Q-P0-003** `mock.Provider.publish` raced with `Disconnect`'s `close(p.events)`: publish read `closed=false`, released the mutex, then Disconnect closed the channel, then publish sent on the closed channel → panic. Fixed by holding `p.mu` across publish + close (mirrors `discord.Provider`'s M3-P3-004 invariant). `Connect`'s `EventConnected` publish also now runs under the lock.
- **M8-C-P0-001** `cmd/agentchat/cmds/send.go` returned raw `fmt.Errorf` instead of `errcode.New(InvalidArgument, …)`, so input errors exited with code 1 instead of 22. Switched to `errcode.New(errcode.InvalidArgument, …)`.

### P1 — security hardening (audit's P1 + the S-P2 items I scoped up)

- **M8-S-P1-001** `DecodeJSON` now wraps `r.Body` in `http.MaxBytesReader(nil, r.Body, MaxRequestBodyBytes=1 MiB)` and surfaces oversize as `errcode.PayloadTooLarge` (HTTP 413). Authenticated callers can no longer OOM the daemon with a multi-GB body. Test: `TestDecodeJSONRejectsOversizeBody` in `internal/api/m8_test.go`.
- **M8-S-P1-002** `crypto.LoadOrCreateMasterKey` re-tightens existing-file mode to 0o600 on every load (mirroring `config.EnsureDataRoot`). A backup-restore or accidental chmod can no longer leave the AES master key world-readable while still in use. Test: `TestLoadOrCreateMasterKeyRetightensExistingMode`.
- **M8-S-P2-002** Attachment cache dirs now `0o700` (was `0o755`); `Chmod 0o600` applied to the downloaded file after rename. Defense-in-depth against a data-root opened up for log bundling.
- **M8-S-P2-003** `Downloader.fetchOne` now verifies `n == a.Size` after the copy (skipped when `a.Size == 0`). A CDN that lies about Content-Length or a Discord-side bug producing inconsistent sizes can no longer leave a row whose `Size` doesn't match the bytes on disk.
- **M8-S-P2-004** `SendMessage` attachment pre-flight uses `os.Lstat` and rejects symlinks. Closes the TOCTOU window where an attacker with write access to the parent dir could swap the target between our stat and the bot.Provider open. Test: `TestPhase3M8AttachmentRejectsSymlink` in `internal/api/m8_phase3_audit_test.go`.
- **M8-S-P2-006 / Q-P1-005** `safeFilename` now whitelists `[A-Za-z0-9._-]`, replaces anything else with `_`, strips leading dots, and caps length at 80 bytes.
- **M8-S-P2-008** `state.Bus.Subscribe` caps concurrent subscribers per account at `MaxSubscribersPerAccount = 8`, returning new `errcode.ResourceExhausted` (HTTP 429) on overflow.
- **M8-S-P2-009** `MarkAnnouncementRead` and `mutateMessageState` collapse `NotFound` into `PermDenied` so the read-state mutation cannot be used to enumerate ids. (Updated `TestAckAnnouncementUnknownIDReturns404` to assert 403.)
- **M8-S-P2-012** `priority="system"` requires admin role. Non-admin → `PermDenied`. The gate fires before the membership probe so a non-member impersonator never hits the membership oracle either. Test: `TestPhase3M8PriorityForbidsSystemForUser`.
- **M8-S-P2-013** `DebugSend` writes an `audit.ActionDebugSend` row on every successful invocation. Operators can no longer use the diagnostic surface to bypass the audit log.

### P1 — code quality

- **M8-Q-P1-003** Downloader `cap` → `maxBackoff` (no more shadowed builtin).
- **M8-Q-P1-004** Downloader fsyncs the temp file (`tmp.Sync()`) before close + rename. Power-loss after rename no longer can leave a zero-byte file under a fully-populated row.
- **M8-Q-P1-007** `errcode.WithDetails` now always allocates a fresh map and assigns it back, so the receiver's Details map is never mutated by subsequent `.WithDetails(...)` calls.
- **M8-Q-P1-008 / M8-T-P1-004 / M8-T-P1-005** `crypto.APITokenCost` switched from `const` to `var`. `internal/api/testmain_test.go` and `pkg/client/testmain_test.go` lower it to `bcrypt.MinCost` for the test binary. Effect: `internal/api` test wall 92s → 4s, `pkg/client` 27s → 0.8s. Under `-race`: `internal/api` 1069s → 17s, `pkg/client` 334s → 5s (~63× / 67× speedup respectively). The Makefile `test-race` timeout was bumped from 20m → 45m so a future slow VM still survives.
- **M8-Q-P1-009** Deleted dead `helpers.AuditOrFail` (and its `audit` / `auth` imports). Was marked Deprecated since M2-P3-012.
- **M8-Q-P1-010** Dropped unused `svc *account.Service` parameter from `SetDiscord` and `ListRooms`. Removed the now-unused `account` import from `rooms.go`.
- **M8-Q-P1-014** Dropped `_time_format=sqlite` from the SQLite DSN — that pragma is `mattn/go-sqlite3` dialect and was silently ignored by `modernc.org/sqlite`. We store Unix seconds via `nullableUnix` anyway.
- **M8-Q-P1-015** `publishRoomMembers` logs WithTx failures via `slog.Warn` instead of swallowing them with `_ =`.
- **M8-Q-P1-016** `DecodeJSON`'s error message now includes the underlying cause text (e.g. `decode request body: json: unknown field "foo"`) so a CLI user can tell what they typo'd. Code stays `InvalidArgument`.

### P1 — build / ship

- **M8-B-P1-001** Makefile stamps a version derived from `git describe --tags --dirty --always` (or `dev`) into both binaries via `-X .../cmds.Version=$(VERSION)`. `agentchat version` and `agentchatd version` now report e.g. `eafab8f-dirty` instead of the literal `dev`.
- **M8-B-P1-002** Makefile passes `-trimpath` to both `go build` invocations. Developer `$HOME` paths no longer leak into binary debug info.
- **M8-B-P1-003** `test-race` timeout 20m → 45m (the bcrypt cost var fix makes the floor much lower; the larger ceiling protects against slow CI / regressions).
- **M8-B-P1-004** `COVER_PKGS` adds `internal/attachment` so M7's downloader code counts toward total coverage.
- **e2e/m1-smoke.sh** assertion updated from literal `"agentchat dev"` to a prefix match (`agentchat ?*`), with `trap … INT TERM HUP` for cleaner ctrl-C.
- **e2e/m7-smoke.sh** "25MB" comment fixed to "10MB" (matches the M7 follow-up code change).

### P1 — CLI / UX

- **M8-C-P1-001** Dropped the inert `--no-color` persistent flag — it was registered but never read by any renderer. Reintroduce when colorized output exists.
- **M8-C-P1-002** `agentchat version` honors `--json` and emits `{"binary":"agentchat","version":"..."}`.

### P1 — docs ↔ code drift (reconciled)

- **M8-D-P1-001** `docs/02-requirements-final.md` §3.4: "~25MB / 文件" → "**当前 10 MB / 文件**" with the Discord 2024-09 backstory; same edit in the §"非目标" table.
- **M8-D-P1-002** `docs/04-roadmap.md` §8 path layout: `<room-id>/<msg-id>/<filename>` → `<message-id>/<attachment-id>/<filename>`, plus notes on the 0o700/0o600 perm fix and the size-verify + fsync changes.
- **M8-D-P1-003** Roadmap §8 "HEAD pre-check" → noted as not implemented (covered by LimitReader + size verify).
- **M8-D-P1-004** Roadmap §2 "Go 1.22+" → "Go 1.25+".
- **M8-D-P1-005** Roadmap §8 endpoint reference `POST /v1/messages` → `POST /v1/rooms/{id}/messages`.
- **M8-D-P1-006** `README.md` status block: M7 closed, M8 in progress; full fix list inline.
- **M8-D-P1-007** `docs/00-overview.md` milestone-progress block extended from M3 to M3–M8.
- **M8-D-P1-009** Roadmap §3 path `internal/store/migrations/` → `internal/store/sqlite/migrations/`.

## 3. Deferred to M9

The audits surfaced more than M8 should tackle in one milestone. Items
explicitly deferred (with rationale) — see `M8-findings.md` for the
full table.

- **M8-S-P2-001** Bot-token plaintext as `string` — fundamentally hard in Go without `unsafe`. Defense-in-depth only.
- **M8-S-P2-005** Arbitrary daemon-side `attachment.path` — needs design (prefix allow-list vs. admin-only vs. client-side upload).
- **M8-S-P2-007** Global rate-limit — needs config + httprate decision.
- **M8-S-P2-010** Audit-log pruner — needs retention-policy design.
- **M8-S-P2-011** `mention_all` per-room policy — needs design.
- **M8-S-P2-014/015** Bus.mu serialization throughput; token-ID lookup timing.
- **M8-Q-P1-001** `Bus.Publish` debounce timer-reset race (extra fire() is idempotent — defer).
- **M8-Q-P1-011 / Q-P1-012** Bus nil-check inconsistency, untracked goroutines + shared WaitGroup. Need a daemon-shutdown design touch.
- **M8-Q-P1-013** `Connector.Disconnect` blocks `account.delete` forever on error → needs an admin `force-offline` endpoint.
- **M8-Q-P1-006** Outbound 10 MB vs inbound 50 MB asymmetry → config-driven attachment cap.
- **M8-Q-P1-017/018** `pkg/client` query-string url.QueryEscape; WriteJSON Content-Type on 204. Cosmetic until non-UUID query values appear.
- **M8-Q-P2-***: long handlers, magic consts, ctx-ignore, enum reasons, etc.
- **M8-C-P1-003** EPIPE handling for `watch state` / `debug events` pipelines.
- **M8-C-P1-***: flag-vs-positional `admin account` cleanup, destructive-op `--yes` flags, naming consistency for `ack-system` / `system-announcements`.
- **M8-B-P1-***: `.github/workflows/ci.yml`, `govulncheck` target, `fmt-check` target, smoke-script `trap INT TERM HUP` rollout to M2–M7.
- **M8-T-P0-001/002 + M8-T-P1-001/002/003** sqlite repo direct tests (announcement / system_announcement / attachment), downloader lifecycle test, pkg/client M6 method tests, output formatter golden tests.
- **M8-T-P1-006/007** Hard-coded sleeps in bus/m3/m4 tests (current race time already comfortable; defer to taste).

## 4. Verification

All run on the M8 working tree (`HEAD = eafab8f-dirty` + M8 edits).

| target | result |
|--------|--------|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l .` | clean |
| `go test ./...` (no race) | all packages green, ~5s wall total |
| `make cover` | total 67.4% (was 67.3% — unchanged; sqlite repo tests deferred) |
| `make test-race` | all packages green; api 17s, pkg/client 5s (was 1069s / 334s pre-M8) |
| `make smoke` (M1 → M7) | all 7 scripts pass; m1 version-prefix matcher works, m7 message updated |
| Real Discord E2E | normal send ✓, attach real file ✓, **attach symlink rejected** ✓, admin `--priority system` ✓, M6 announcement mirror ✓ |

## 5. Phase 3 verdict

Per `feedback_phase3_user_driven.md`, this phase's verdict is the
**user's** to call, not the developer's. The work above is the
developer-side completion claim; the user runs the audit (`/ultrareview`,
manual code review, or fresh agent pass) and decides PASS / FAIL.
