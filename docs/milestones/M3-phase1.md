# M3 — Phase 1 Report (Implementation)

> Companion to `M3-phase2.md` (testing). Phase 1+2 executed as one
> continuous block per `05-engineering-workflow.md`.
>
> Per the user's M3 plan: this milestone stops here so the operator
> can run the real-Discord verification. **Phase 3 audit happens
> after that verification.**

**Date:** 2026-05-13
**Milestone scope:** `04-roadmap.md` §4 — Bot abstraction layer +
Discord adapter + account online/offline lifecycle.

## 1. Goal recap

Define a platform-agnostic `bot.Provider` interface, implement it for
Discord via `bwmarrin/discordgo`, and wire it into the daemon so that
admin accounts can:

1. Have a Discord bot token attached (`set-discord`, AES-GCM encrypted
   at rest)
2. Transition to `online` (connect to Discord, capture identity)
3. Transition back to `offline` (disconnect cleanly)
4. Send a message via a live Provider (`debug send`)
5. Stream live events from a live Provider (`debug events`, NDJSON)

The Discord adapter is **compile-clean and architecturally complete**
but is **not exercised by automated tests** — that's the operator's
responsibility for this milestone (real bot token + real guild + real
intents).

## 2. Files added / modified

### New packages

| Package | Purpose |
|---------|---------|
| `internal/bot/` | Platform-agnostic Provider interface (`provider.go`) + value types (`types.go`) + Event types (`events.go`) |
| `internal/bot/mock/` | Programmable in-memory Provider for tests; tracks every method call so callers can assert side effects |
| `internal/bot/discord/` | `discordgo`-backed Provider; `discord.New(token, hint, Options{})` returns a Provider |
| `internal/connector/` | Lifecycle manager for live Provider instances — does NOT touch the DB (M2-P3-012 style separation of concerns) |

### Extended packages

| Package | Change |
|---------|--------|
| `internal/crypto/` | New `AESGCMEncrypt` / `AESGCMDecrypt` using AES-256-GCM with 12-byte random nonce; ciphertext layout = nonce ‖ ct |
| `internal/audit/` | New action constants: `account.discord_set`, `account.online`, `account.offline` |
| `internal/api/v1/discord.go` | `SetDiscord`, `OnlineAccount`, `OfflineAccount`, `AccountStatus` handlers — all mutations are transactional via `bundler.WithTx` |
| `internal/api/v1/debug.go` | `DebugSend`, `DebugEvents` (NDJSON stream) — M3-only diagnostic surface |
| `internal/api/v1/types.go` | New DTOs: `SetDiscordRequest`, `StatusResponse`, `DebugSendRequest`, `DebugSendResponse` |
| `internal/api/server.go` | `Deps.Connector` and `Deps.MasterKey`; new routes wired |
| `internal/api/middleware/logger.go` | `statusRecorder.Flush()` explicit forward to preserve `http.Flusher` — required for `/v1/debug/events` NDJSON streaming |
| `cmd/agentchatd/cmds/serve.go` | Constructs Connector with the Discord factory; passes Connector + MasterKey to API; defers `Connector.Shutdown` |
| `cmd/agentchat/cmds/admin_account.go` | New subcommands: `set-discord`, `online`, `offline`, `status` |
| `cmd/agentchat/cmds/debug.go` | New `debug` parent + `debug send` + `debug events` |
| `pkg/client/client.go` | Methods: `SetDiscord`, `Online`, `Offline`, `Status`, `DebugSend`, `StreamEvents` + `Event` type |
| `Makefile` | `COVER_PKGS` now includes `internal/connector`; `smoke` target runs M1+M2+M3 |
| `e2e/m3-smoke.sh` | Mock-driven smoke: surface verification only; explicitly documents that real-Discord verification is a manual operator step |

### New top-level files

- `e2e/m3-smoke.sh`
- `docs/milestones/M3-phase1.md` (this file)
- `docs/milestones/M3-phase2.md` (companion)

## 3. Dependencies added

Per workflow §1.8 (URL + version + check date):

| Package | Version | Docs URL | Checked |
|---------|---------|----------|---------|
| `github.com/bwmarrin/discordgo` | v0.29.0 (2025-05-24) | https://github.com/bwmarrin/discordgo + https://github.com/bwmarrin/discordgo/blob/master/examples/pingpong/main.go | 2026-05-13 |
| `github.com/gorilla/websocket` | v1.4.2 (indirect via discordgo) | — | — |

## 4. Architecture decisions made in flight

1. **Provider interface is small.** M3 defines only what M3 + M4 will
   need: `Connect/Disconnect`, `Status`, `Identity`, `SendMessage`,
   `CreateChannel`/`DeleteChannel`, `AddMember`/`RemoveMember`,
   `FetchHistory`, and an `Events()` channel. Reactions, attachments,
   and threads are M4–M7 territory.
2. **Connector does NOT touch the DB.** After the M2-P3-012 fix
   showed how easily mutation + audit can drift, the Connector here
   only manages in-memory Provider instances. The API handler reads
   the encrypted token, calls `Connector.Connect`, then writes the
   account lifecycle and audit row inside `bundler.WithTx`. On tx
   failure the just-connected Provider is best-effort disconnected
   so the two stores agree.
3. **Identity-after-Ready.** Discord's gateway Ready event carries the
   bot's `User`. The adapter blocks `Connect` until either Ready
   arrives (success path) or `ReadyTimeout` (default 30s, configurable
   via `Options`) elapses. Without this, `SendMessage` could race
   ahead of the gateway handshake and fail mysteriously.
4. **AES-GCM blob layout = `nonce ‖ ciphertext`.** Standard pattern;
   the encrypt helper appends ct onto the nonce (`gcm.Seal(nonce, …)`)
   and the decrypt helper slices the leading 12 bytes back out. Wrong
   key / tampered ct maps to `errcode.AuthInvalid`.
5. **`statusRecorder.Flush()` explicit forward.** Embedded-interface
   method promotion does NOT carry `Flush` over: `http.ResponseWriter`
   does not include it in its method set, so a wrapper struct must
   delegate manually. The logger middleware was silently breaking the
   streaming endpoint until I wired this — caught by `TestDebugEventsStreamsNDJSON`.
6. **`unknown_fields` rejection unchanged.** Existing M2 behavior
   (`DisallowUnknownFields` in `DecodeJSON`) carries over to the new
   `set-discord` and `debug send` endpoints — typos in request fields
   surface as 400.
7. **Event NDJSON envelope** carries a `type` discriminator and only
   the fields relevant to that type. Avoids the union-type ambiguity
   that bit me when I drafted Event as a plain interface{}.
8. **Privileged intents documented.** The Discord adapter requests
   `MessageContent` and `GuildMembers`. Operators MUST enable these
   in the Developer Portal per bot application, or messages will
   arrive with empty content. README + smoke script note this.

## 5. Intentionally deferred (M4+)

- **GuildID** on the Discord adapter: `Options.GuildID` exists but
  defaults to empty; `CreateChannel` currently returns
  `InvalidArgument` when unset. M4's rooms subsystem will source this
  from config.
- **Bot identity persistence**: after `online` the adapter returns the
  Discord-side `Identity` (username, avatar URL) but we do NOT
  persist it on the account row. M4 will, so room views can render
  the right bot name without an extra Discord API call.
- **Real-Discord automated tests**: out of scope by design. M3's
  Phase 3 audit runs after the operator does a manual real-Discord
  verification.

## 6. Demo (real-Discord flow, manual)

```bash
# Prerequisites: create a Discord application + bot in the Developer
# Portal, enable MessageContent + GuildMembers intents, and add the
# bot to your guild via the OAuth invite URL. Note the bot token and
# a channel ID.

make build
./bin/agentchatd serve --data-root /tmp/m3demo
# Capture the printed AGENTCHAT_TOKEN.

# In another shell:
export AGENTCHAT_TOKEN=<paste>
export AGENTCHAT_HOME=/tmp/m3demo

./bin/agentchat admin account create --name agent1 --role user
ACC=$(./bin/agentchat admin account list --json | jq -r '.[]|select(.name=="agent1").id')
./bin/agentchat admin account set-discord "$ACC" --bot-token "<discord-bot-token>"
./bin/agentchat admin account online "$ACC"
./bin/agentchat admin account status "$ACC"  # provider_status: online

./bin/agentchat debug send --account "$ACC" --channel "<channel-id>" --text "hello from agentchat M3"
# Verify the message appears in Discord.

./bin/agentchat debug events --account "$ACC"  # streaming NDJSON
# Send a message in Discord from another user — observe message_new in this stream.
```

## 7. Layout snapshot (M3 additions)

```
internal/bot/                provider.go types.go events.go doc.go
internal/bot/mock/           mock.go
internal/bot/discord/        discord.go
internal/connector/          connector.go connector_test.go
internal/crypto/             aesgcm.go aesgcm_test.go            (existing pkg, extended)
internal/api/v1/             discord.go debug.go                  (existing pkg, extended)
internal/api/middleware/     logger.go                            (Flusher forward)
internal/api/                server.go m3_test.go                 (existing, extended)
cmd/agentchat/cmds/          admin_account.go debug.go            (existing, extended)
cmd/agentchatd/cmds/         serve.go                             (Connector wiring)
pkg/client/                  client.go m3_test.go                 (existing, extended)
e2e/                         m3-smoke.sh
docs/milestones/             M3-phase1.md M3-phase2.md
```
