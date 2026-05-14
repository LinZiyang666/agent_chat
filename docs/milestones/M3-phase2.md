# M3 — Phase 2 Report (Testing)

> Companion to `M3-phase1.md`. Phase 1+2 executed as one continuous
> block per `05-engineering-workflow.md`. The user gated this report:
> the **Discord adapter is not automatically tested**; the operator
> verifies it against real Discord, and only then does Phase 3 audit
> the milestone.

**Date:** 2026-05-13
**Verification command:** `make fmt vet test-race smoke cover`
**Result:** all green.

## 1. Strategy

| Layer | How we tested it |
|------|------|
| `internal/bot` interface + value types | Compiled-in `bot.Provider` interface check in `internal/bot/discord/discord.go` (`var _ bot.Provider = (*Provider)(nil)`); semantic coverage comes through the mock. |
| `internal/bot/mock` | Exercised via `internal/connector`, `internal/api`, and `pkg/client` tests — its programmable side-effect ledgers + InjectMessage/InjectEvent are the test substrate. |
| `internal/crypto/aesgcm` | 7 unit tests: round-trip, nonce variability, wrong-key rejection, tampered-ct rejection, short-blob rejection, wrong-key-size rejection, empty plaintext. |
| `internal/connector` | 7 unit tests with a mock factory: happy connect, double connect conflict, disconnect happy, disconnect-not-online conflict, subscribe receives events, channel closes on disconnect, shutdown disconnects all. |
| `internal/api/v1/discord.go` + `debug.go` | 6 HTTP integration tests through `httptest.NewServer` with a mock-factory Connector: set-discord round-trip + audit, set-discord empty-token rejected, online-without-token rejected, online happy + identity surfaced, debug send forwarding, NDJSON event streaming. |
| `pkg/client` M3 surface | 7 integration tests via a real Unix socket daemon stack (also mock factory): SetDiscord+Status, online/offline lifecycle, online-without-token, DebugSend, StreamEvents, DebugSend-not-online error mapping, NDJSON parsing sanity. |
| `cmd/agentchat/cmds` Discord commands | Help-text rendering verified in `e2e/m3-smoke.sh`; full command paths are covered by the daemon-driven smoke. |
| `cmd/agentchatd/cmds/serve.go` Connector wiring | Implicitly exercised by `e2e/m3-smoke.sh` (which boots a real daemon and sets a fake token). |
| `internal/bot/discord` (real adapter) | **Manual** — out of scope for automated tests, per M3's "stop here for real-Discord verification" plan. The adapter compiles, satisfies the `bot.Provider` interface, and follows discordgo's documented patterns. |

## 2. Test files added

| File | Tests |
|------|-------|
| `internal/crypto/aesgcm_test.go` | 7 cases: round-trip, nonce-varies, wrong-key, tampered-ct, short-blob, bad-key-size, empty-plaintext |
| `internal/connector/connector_test.go` | 7 cases (lifecycle + subscriptions + shutdown) |
| `internal/api/m3_test.go` | 6 cases: `TestSetDiscordPersistsTokenAndAudits`, `TestSetDiscordRejectsEmptyToken`, `TestOnlineWithoutTokenRejected`, `TestOnlineHappyPath` (online + offline round trip), `TestDebugSendForwardsToProvider`, `TestDebugSendNotOnline`, `TestDebugEventsStreamsNDJSON` |
| `pkg/client/m3_test.go` | 7 cases: SetDiscord+Status, online/offline, online-without-token, DebugSend, StreamEvents, DebugSend-not-online, NDJSON framing |
| `e2e/m3-smoke.sh` | 1 shell script — **API-surface verification, not mock-driven**: it boots the real daemon (with the real Discord factory) and asserts on the *failure* paths that do not require a live gateway: set-discord persists, status reports has_bot_token, offline-when-not-online rejected, debug send/events rejected when not online, help text renders |

## 3. `go test -race ./...` outcome

All packages pass with `-race`. No race warnings. Wall-clock continues
to be dominated by bcrypt cost 12 in the auth and account suites
(unchanged from M2).

## 4. Coverage (after M3)

| Package | Lines | Notes |
|---------|------|------|
| internal/account | **90.4%** | unchanged from M2 |
| internal/api | **100.0%** | new M3 handlers fully covered by `m3_test.go` |
| internal/audit | **84.2%** | new action constants don't change coverage |
| internal/auth | **83.0%** | unchanged |
| internal/cliutil | **73.7%** | unchanged |
| internal/config | **84.7%** | unchanged |
| internal/connector | **90.9%** | new — only the shutdown-of-empty branch is uncovered |
| internal/crypto | **82.0%** | up from 79.4% — new aesgcm.go covered |
| internal/errcode | **95.3%** | unchanged |
| internal/store/sqlite | **74.0%** | unchanged |
| pkg/client | **79.7%** | new M3 methods covered; streaming + error decode paths exercised |
| cmd/agentchat/cmds | 20.2% | binary entry — covered end-to-end via smoke |
| cmd/agentchatd/cmds | 27.2% | binary entry — covered end-to-end via smoke |
| **total** | **74.8%** | |

All 11 business packages exceed the 70% gate; `internal/bot/discord`
is not listed because it has no automated tests (operator-verified
this milestone).

## 5. Edge cases exercised

### Crypto

- Distinct nonces on repeat `Encrypt` of identical plaintext
- Wrong key returns `AuthInvalid`, not a panic
- Tampered ciphertext returns `AuthInvalid`
- Blob shorter than nonce length returns `InvalidArgument`
- Key with wrong length rejected at Encrypt time
- Empty plaintext round-trips

### Connector

- Double-Connect on the same account returns `Conflict`
- Disconnect on never-connected account returns `Conflict`
- Subscribe receives `EventConnected` from the mock's `Connect` path
- Inject a message and assert it arrives on the subscription
- Subscriber channel is closed when the underlying Provider disconnects
- Shutdown disconnects every live Provider

### API

- `set-discord` round-trip persists the token (encrypted) and writes
  `account.discord_set` to the audit log
- Empty `bot_token` rejected as `INVALID_ARGUMENT`
- `online` rejected when no token has been set
- `online` happy path returns `provider_status=online` and the
  resolved bot identity
- `offline` round-trip transitions lifecycle and emits the audit row
  in the same transaction
- `debug send` forwards to the mock Provider and the mock records the
  send
- `debug send` against a not-online account returns `CONFLICT`
- `debug events` opens an NDJSON stream; injected messages arrive
  with `type: message_new` and the right payload

### pkg/client

- SetDiscord + Status full round trip
- Online + Offline lifecycle transitions
- DebugSend success + DebugSend-not-online error mapping
- StreamEvents observed `message_new` end-to-end
- Wire-format NDJSON framing parses cleanly with multiple events

### e2e (`e2e/m3-smoke.sh`)

- set-discord persists the token
- status reflects `has_bot_token: true` after set
- status reports `provider_status: offline` before online
- offline-when-not-online rejected with `CONFLICT`
- debug send when not online rejected with `CONFLICT`
- debug events when not online rejected with `CONFLICT`
- `--help` works for every new subcommand

## 6. Bugs found and fixed during testing

1. **`statusRecorder.Flush()` was missing** — the logger middleware's
   response-writer wrapper hid the `http.Flusher` capability because
   Go does not promote methods that aren't in the embedded interface's
   method set. `/v1/debug/events` was returning 500 ("response writer
   does not support flushing"). Fixed by adding an explicit `Flush()`
   forward in `internal/api/middleware/logger.go`.

2. **Stale `IssueVia` shape** (pre-M3) had bcrypt inside the
   tx — already fixed in M2-P3-014 (`PrepareToken` + `PersistTokenVia`).
   Mentioned here because the M3 work touched the same handler and
   confirmed the M2 fix is still in effect.

## 7. Known weaknesses tests cannot cover

- **Real discordgo behavior under live network conditions.** Outside
  M3 automated scope by design — verified by the operator.
- **Privileged-intent rejection.** If the operator forgets to enable
  `MessageContent` in the Developer Portal, `MessageCreate` events
  will arrive with empty content. The adapter passes the empty
  content through faithfully; the operator notices and fixes the
  portal toggle.
- **Discord rate limits.** `discordgo` handles 429 retries internally;
  the adapter does not add its own retry layer.
- **Multi-guild scenarios.** Architecture mandates a single guild;
  not tested.

## 8. Phase 2 exit checklist

```
[x] unit tests for every non-trivial new business package
[x] mock-driven integration tests via httptest + Unix socket
[x] e2e script covers the new CLI commands' --help and error surface
[x] go test ./... -race clean
[x] coverage >= 70% on each code-bearing business package
[x] phase1.md written
[x] phase2.md written (this file)
```

## 9. Hand-off (manual real-Discord verification, then Phase 3)

The operator will:

1. Create a Discord application + bot per `M3-phase1.md` §6.
2. Run the demo flow in §6 with the real bot token + channel id.
3. Confirm:
   - `admin account online` returns within ~30s with
     `provider_status=online` and a real bot Identity.
   - `debug send` makes a message appear in Discord.
   - `debug events` streams `message_new` when a real user types in
     the channel.
   - `admin account offline` cleanly tears the session down.
4. When the operator is satisfied, Phase 3 audit runs against this
   tree.
