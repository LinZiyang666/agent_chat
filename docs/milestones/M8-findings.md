# M8 — Findings index (consolidated)

> Aggregated from 6 parallel audit agents (security, code-smell,
> tests, docs, CLI, build/ship). IDs assigned here; resolution
> status tracked alive in this file. Final report is `M8-phase3.md`.

## Severity rubric

- **P0** — actively exploitable bug, data corruption, daemon crash on reachable input.
- **P1** — bug behind unusual-but-legal input, info disclosure, DoS, dev-facing wrong mental model.
- **P2** — defense-in-depth, naming, dead code, cosmetic drift.

## Phase plan

- **A** Critical bugs (3 P0)
- **B** Test speedup (race time -90%)
- **C** Security hardening (P1 + selected P2)
- **D** Code-quality fixes
- **E** Build / ship
- **F** CLI / UX
- **G** Docs ↔ code reconciliation
- **H** Coverage backfill (test agent's P0 sqlite repos + downloader lifecycle)

## Findings table

| ID | Agent | Sev | Title | Status |
|----|-------|-----|-------|--------|
| M8-S-P1-001 | security | P1 | DecodeJSON unbounded body | ✅ FIXED (helpers.go MaxBytesReader 1 MiB + errcode.PayloadTooLarge + tests) |
| M8-S-P1-002 | security | P1 | master.key existing mode not re-tightened | ✅ FIXED (os.Chmod 0o600 + test) |
| M8-S-P2-001 | security | P2 | Bot-token plaintext as string on Provider | deferred to M9 (hard in Go without unsafe) |
| M8-S-P2-002 | security | P2 | Attachment cache dirs 0o755 / files 0o644 | PLANNED — MkdirAll 0o700 + Chmod 0o600 after rename |
| M8-S-P2-003 | security | P2 | Download has no size verification | PLANNED — verify n == a.Size; also fsync (=Q-P1-006) |
| M8-S-P2-004 | security | P2 | TOCTOU symlink swap between Stat and Open | PLANNED — Lstat & reject symlink |
| M8-S-P2-005 | security | P2 | attachment.path is arbitrary daemon-side path | deferred to M9 (design needed: prefix allow-list vs admin-only) |
| M8-S-P2-006 | security | P2 | safeFilename strictness | PLANNED — whitelist [A-Za-z0-9._-] (=Q-P1-008) |
| M8-S-P2-007 | security | P2 | No global rate-limit | deferred to M9 (config + httprate) |
| M8-S-P2-008 | security | P2 | No per-account watch subscriber cap | PLANNED — cap at 8 |
| M8-S-P2-009 | security | P2 | Enumeration oracle: announcement / message reads | PLANNED — swap membership check first |
| M8-S-P2-010 | security | P2 | No audit-log pruner | deferred to M9 |
| M8-S-P2-011 | security | P2 | mention_all not policy-gated | deferred to M9 (per-room policy) |
| M8-S-P2-012 | security | P2 | priority="system" not admin-gated | PLANNED — gate on admin |
| M8-S-P2-013 | security | P2 | DebugSend bypasses audit | PLANNED — write audit row |
| M8-S-P2-014 | security | P2 | Bus.mu serialization at scale | deferred to M9 |
| M8-S-P2-015 | security | P2 | Token-ID lookup timing leak | deferred to M9 (cosmetic) |
| M8-Q-P0-001 | code-smell | P0 | EOF detection dead code | ✅ FIXED (folded into S-P1-001: io.EOF) |
| M8-Q-P0-002 | code-smell | P0 | connector.pump tail deletes subs across generations | PLANNED — generation-guarded pump |
| M8-Q-P0-003 | code-smell | P0 | mock.publish send-after-close race | PLANNED — hold mu across publish+close |
| M8-Q-P1-001 | code-smell | P1 | Bus.Publish racy debounce timer reset | PLANNED — generation sentinel |
| M8-Q-P1-002 | code-smell | P1 | Connector instances-slot vs Disconnect race | folded into Q-P0-002 |
| M8-Q-P1-003 | code-smell | P1 | Downloader `cap` shadows builtin | PLANNED — rename to maxBackoff |
| M8-Q-P1-004 | code-smell | P1 | Downloader missing fsync | PLANNED — tmp.Sync before Close |
| M8-Q-P1-005 | code-smell | P1 | safeFilename strict | folded into S-P2-006 |
| M8-Q-P1-006 | code-smell | P1 | Outbound 10MB vs inbound 50MB asymmetry | deferred to M9 (config) |
| M8-Q-P1-007 | code-smell | P1 | errcode.WithDetails else-branch bug | PLANNED |
| M8-Q-P1-008 | code-smell | P1 | bcrypt cost in tests | PLANNED — make var + lower in tests |
| M8-Q-P1-009 | code-smell | P1 | AuditOrFail dead code | PLANNED — delete |
| M8-Q-P1-010 | code-smell | P1 | SetDiscord/ListRooms unused svc param | PLANNED — drop |
| M8-Q-P1-011 | code-smell | P1 | bus nil-check inconsistency | PLANNED — standardize (drop redundant checks) |
| M8-Q-P1-012 | code-smell | P1 | Untracked goroutines (publishRoomMembers etc) | PLANNED — shared WaitGroup |
| M8-Q-P1-013 | code-smell | P1 | Connector.Disconnect blocks delete on error | deferred to M9 (force-offline admin endpoint) |
| M8-Q-P1-014 | code-smell | P1 | DSN `_time_format=sqlite` no-op | PLANNED — remove + comment |
| M8-Q-P1-015 | code-smell | P1 | publishRoomMembers swallows tx errors | PLANNED — log via Deps.Log |
| M8-Q-P1-016 | code-smell | P1 | DecodeJSON opaque error message | PLANNED — include cause text |
| M8-Q-P1-017 | code-smell | P1 | pkg/client query strings unescaped | PLANNED — url.QueryEscape |
| M8-Q-P1-018 | code-smell | P1 | WriteJSON sets CT for 204 | PLANNED — branch on status |
| M8-Q-P2-*   | code-smell | P2 | ~12 cosmetic items (long handlers, magic consts, ctx-ignore, enum reasons, etc.) | deferred to M9 |
| M8-T-P0-001 | tests | P0 | sqlite/announcement_repo + system_announcement_repo 0% direct coverage | PLANNED — add sqlite_m6_test.go |
| M8-T-P0-002 | tests | P0 | sqlite/attachment_repo 0% direct coverage | PLANNED — add sqlite_m7_test.go |
| M8-T-P1-001 | tests | P1 | Downloader.Start/Shutdown/run untested | PLANNED — add lifecycle test |
| M8-T-P1-002 | tests | P1 | pkg/client M6 methods 0% covered | PLANNED — add pkg/client/m6_test.go |
| M8-T-P1-003 | tests | P1 | cmd/agentchat/cmds/output.go formatters 0% | PLANNED — pure-fn tests |
| M8-T-P1-004 | tests | P1 | api tests serial — biggest race-time cost | PLANNED — add t.Parallel() (with bcrypt cost var) |
| M8-T-P1-005 | tests | P1 | pkg/client tests serial | PLANNED — add t.Parallel() |
| M8-T-P1-006 | tests | P1 | bus_test 300ms sleep | PLANNED — iteration count |
| M8-T-P1-007 | tests | P1 | m4 ingester 150ms sleep | PLANNED — Eventually |
| M8-T-P2-*   | tests | P2 | ~8 cosmetic test items | partial — drop _,_= patterns where cheap |
| M8-D-P1-001 | docs | P1 | 25MB → 10MB cap | PLANNED |
| M8-D-P1-002 | docs | P1 | Attachment path layout | PLANNED |
| M8-D-P1-003 | docs | P1 | Downloader poll vs event | PLANNED |
| M8-D-P1-004 | docs | P1 | Go 1.22 → 1.25 | PLANNED |
| M8-D-P1-005 | docs | P1 | POST /v1/messages → POST /v1/rooms/{id}/messages | PLANNED |
| M8-D-P1-006 | docs | P1 | README "M7 next" → M7 done | PLANNED |
| M8-D-P1-007 | docs | P1 | 00-overview milestone progress | PLANNED |
| M8-D-P1-008 | docs | P1 | architecture §4 schema sketch reconciliation | PLANNED |
| M8-D-P1-009 | docs | P1 | roadmap §3 path internal/store/migrations | PLANNED |
| M8-D-P1-other | docs | P1 | ~7 more drifts (Provider iface, lifecycle archived, secondary UI, reactions, health-bar, etc.) | partial — note as deferred features |
| M8-C-P0-001 | cli | P0 | send.go raw fmt.Errorf wrong exit code | PLANNED |
| M8-C-P1-001 | cli | P1 | --no-color is a no-op | PLANNED — drop or implement |
| M8-C-P1-002 | cli | P1 | version doesn't honor --json | PLANNED |
| M8-C-P1-003 | cli | P1 | watch / debug events don't handle EPIPE | PLANNED |
| M8-C-P1-other | cli | P1 | flag-vs-positional inconsistency, etc. | partial |
| M8-B-P1-001 | build | P1 | No version stamped via -X | PLANNED |
| M8-B-P1-002 | build | P1 | No -trimpath | PLANNED |
| M8-B-P1-003 | build | P1 | test-race timeout 20m tight | PLANNED — bump 45m |
| M8-B-P1-004 | build | P1 | COVER_PKGS missing internal/attachment | PLANNED |
| M8-B-P1-005 | build | P1 | smoke scripts trap EXIT only | PLANNED — INT TERM HUP |
| M8-B-P1-other | build | P1 | CI / govulncheck / fmt-check | deferred to M9 (needs design) |

Source full reports:
- `/tmp/m8-security-findings.md`
- `/tmp/m8-codequality-findings.md`
- `/tmp/m8-test-findings.md`
- `/tmp/m8-docs-findings.md`
- `/tmp/m8-cli-findings.md`
- `/tmp/m8-build-findings.md`
