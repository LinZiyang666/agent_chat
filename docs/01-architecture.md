# 01 · 系统架构

> 描述 **当前代码** 的组件、数据模型、关键不变量、并发模型。读这篇之前先扫一眼 [`00-overview.md`](./00-overview.md) 拿到整体语境。

---

## 1. 进程拓扑

```
   ┌─────────────────────────────────────────────────────────────┐
   │  Discord (gateway WSS + REST)                               │
   └─────────────▲───────────────────▲──────────────▲────────────┘
                 │ bot-A WS          │ bot-B WS     │ CDN HTTPS
                 │                   │              │
   ┌─────────────┴───────────────────┴──────────────┴────────────┐
   │ agentchatd  (single process per data-root)                  │
   │                                                             │
   │  ┌───────────────┐   ┌─────────────────┐   ┌─────────────┐  │
   │  │ Connector     │   │ message.Ingester│   │ attachment  │  │
   │  │  ─ A: provider │──►│ (1 goroutine    │◄──│ Downloader  │  │
   │  │  ─ B: provider │   │  per attached   │   │ (1 goroutine│  │
   │  │  ─ subs map    │   │  account)       │   │  poll 2s)   │  │
   │  └──┬────────────┘   └──┬──────────────┘   └─────┬───────┘  │
   │     │ events             │ WithTx commits         │ HTTP GET │
   │     ▼                    ▼                        ▼          │
   │  ┌───────────────────────────────────────────────────────┐   │
   │  │ store.Bundle  ←→  SQLite (modernc, WAL, single conn) │   │
   │  └────┬──────────────────────────────────────────────────┘   │
   │       │ Publish(accountID)                                   │
   │       ▼                                                     │
   │  ┌───────────────┐    ┌─────────────────────────────┐       │
   │  │ state.Bus     │    │ HTTP server on UDS          │       │
   │  │  ─ 200ms      │───►│  ─ /v1/...                  │◄──┐   │
   │  │    debounce   │    │  ─ /v1/state/watch (NDJSON) │   │   │
   │  │  ─ version++  │    │  ─ /v1/debug/events         │   │   │
   │  │  ─ subs cap 8 │    └─────────────────────────────┘   │   │
   │  └───────────────┘                                       │   │
   └──────────────────────────────────────────────────────────┼──┘
                                                              │ UDS
                                                  ┌───────────┴────┐
                                                  │ agentchat CLI  │
                                                  │ (cobra, JSON)  │
                                                  └────────────────┘
```

- **一个 data-root = 一个 daemon**。`<data-root>/agentchatd.lock` 用 `flock(LOCK_EX|LOCK_NB)` 抢占（[`cmd/agentchatd/cmds/datalock.go`](../cmd/agentchatd/cmds/datalock.go)），第二份立刻 `CONFLICT`。
- **CLI 不持久连接**。除 `watch state` / `debug events` 这两个长流以外，每个命令一次 HTTP 请求即退出。
- **UDS 文件 `0600`**，外加 data-root `0700`：只有 daemon 进程的 owner UID 能连。
- **没有 TLS / 网络监听**：所有传输用 Unix domain socket，token 仍是 bearer（防同机其它用户读 token、不防同机同 UID）。

---

## 2. 顶层包结构

```
cmd/
  agentchatd/        daemon binary (main.go) + cmds/ (cobra wiring)
  agentchat/         CLI binary (main.go)   + cmds/ (cobra wiring)
internal/
  account/           Account CRUD + lifecycle policy
  api/               HTTP transport
    server.go        chi router; wires Deps → routes
    middleware/      Logger, Recover
    v1/              JSON DTOs + handlers (accounts, tokens, rooms, ...)
  attachment/        Background downloader for inbound files
  audit/             Action enum + Recorder; writes via Bundler.WithTx
  auth/              API token format + Manager + bearer middleware
  bot/               Provider interface; concrete in bot/discord, bot/mock
    discord/         discordgo adapter + Prober
    mock/            In-memory test double + Prober
    events.go        EventConnected / EventDisconnected / EventMessageNew / EventChannelDeleted
    mentions.go      ParseMentions(@<name> → <@bot_user_id>)
    types.go         Identity / Message / MsgAttachURL / SendOptions / IdentityProber
  cliutil/           Stable stderr error format + TTY detect
  config/            TOML + env overlay, EnsureDataRoot(0o700)
  connector/         Provider lifecycle + event fan-out (per-account mutex'd map)
  crypto/            bcrypt for API tokens; AES-GCM for bot tokens; master.key boot
  errcode/           Code enum + ExitCode + HTTPStatus
  message/           Inbound ingester (MESSAGE_CREATE / CHANNEL_DELETE → SQL + Publish)
  state/             Aggregator (build Snapshot) + Bus (debounce/fan-out)
  store/             Repo interfaces + Bundler.WithTx contract
  store/sqlite/      modernc/sqlite implementation + embedded migrations
pkg/
  client/            Public Go client (CLI consumes; agents may import)
```

Empty `doc.go` 文件存在于多个包（`announcement`, `room`, `health`），是为更易演进的占位；不携带逻辑。

---

## 3. 数据模型（SQLite 实体）

迁移文件在 [`internal/store/sqlite/migrations/`](../internal/store/sqlite/migrations/)，启动时通过 `schema_migrations` 自动跑。

```
accounts (M2 / M4 / M9 演化)
├─ id (uuidv7)
├─ name UNIQUE
├─ role         ∈ {admin, user}
├─ lifecycle_state ∈ {created, online, offline, archived, deleted}
├─ bot_token_enc BLOB    (AES-GCM(master.key, discord_bot_token))
├─ bot_user_id           (Discord snowflake，set-discord 时通过 Prober 写入)
└─ created_at / updated_at  (Unix sec, UTC)

tokens (M2)        ─ FK accounts ON DELETE CASCADE
├─ id (uuidv7 hex, 32 chars; tokens encode as agch_<id>_<base64url 32B>)
├─ token_hash BLOB       (bcrypt cost 12)
└─ created_at / revoked_at / last_used_at

audit_log (M2)             ─ append-only
├─ id (uuidv7)
├─ account_id              (actor, 或 "system" for daemon-driven sweeps)
├─ action                  (audit.Action 字符串)
├─ target                  (resource id)
├─ payload JSON
└─ created_at

rooms (M4)
├─ id (uuidv7)
├─ discord_channel_id UNIQUE
├─ name / archived (0/1) / created_at / updated_at

memberships (M4)        ─ PK (account_id, room_id)
└─ subscribed (0/1) / joined_at
   (no row = not joined; subscribed=1 = 主状态 active; 0 = 旁观)

messages (M4 / M6 / M9)
├─ id (uuidv7, time-ordered tie-breaker)
├─ room_id FK
├─ author_account_id FK NULLABLE          (NULL = 外部 Discord 用户)
├─ discord_msg_id UNIQUE                  (send + ingest 汇合键)
├─ content / reply_to_msg_id NULLABLE
├─ priority ∈ {normal, urgent, system}
├─ created_at / content_hash (sha256 hex)
└─ mention_everyone (0/1)                 (M9 新; M9-P2 删了旧 mention_all / requires_ack)

message_states (M4 / M9-P2)  ─ PK (message_id, account_id)
└─ read_at NULLABLE     (NULL = 未读；M9-P2 删了 replied_at)

message_mentions (M9-P1)  ─ PK (message_id, account_id)
└─ 一行 = 该 account 在该消息中被 @；INSERT OR IGNORE 幂等

reactions (M4)            占位表，无 repo，未来扩展用

announcements (M6)
├─ id (uuidv7)
├─ room_id FK / content / version (单调递增 per room)
├─ created_by NULLABLE / created_at
announcement_reads (M6)   ─ PK (announcement_id, account_id) / read_at  (absence = 未读)

system_announcements (M6) ─ admin-only 写
system_announcement_reads ─ 同上

attachments (M7)
├─ id (uuidv7)
├─ message_id FK
├─ filename / size / mime / discord_url / local_path / sha256 / downloaded_at NULLABLE
└─ idx_attachments_pending = partial index WHERE downloaded_at IS NULL
```

### 3.1 关键约束 / 索引

- `accounts.name UNIQUE`：Discord-authoritative 同步时若新 username 与本地他账号同名，整事务回滚成 `CONFLICT`。
- `messages.discord_msg_id UNIQUE`：send 路径 INSERT 与 gateway ingester INSERT 任一胜出后另一方走 `ApplySendMetadata` / `MergeMentionEveryone` / `AddForMessage` 合并字段。这是 M4-P3-007/010 + M9-P1 的合并路径。
- `idx_messages_room_created (room_id, created_at DESC, id DESC)`：分页 / 最近消息查询用；uuidv7 id 本身就单调，所以同秒并发也有确定性次序。
- `idx_attachments_pending` 是 partial index，仅 `downloaded_at IS NULL` 时建索引——`Downloader` 的 `ListPendingDownloads` 走它。

### 3.2 时间戳

- 全部存为 Unix seconds（INTEGER），出库时 `time.Unix(..,0).UTC()`。
- API DTO 全部 RFC3339 UTC。

---

## 4. 组件与契约

### 4.1 `internal/bot`：平台无关接口

- `Provider` 接口（`provider.go`）定义全部 chat-backend 操作：`Connect / Disconnect / SendMessage / CreateChannel / DeleteChannel / AddMember / RemoveMember / FetchHistory / Events()`。
- `Event` 是密封接口（type switch 消费）：`EventConnected / EventDisconnected / EventMessageNew / EventChannelDeleted`。
- `IdentityProber`（M9-P2）：`Probe(token) → Identity` + `Rename(token, newName)`。Discord 实现走 REST `GET /users/@me` / `PATCH /users/@me`（无 gateway 开销）；mock 用 hint.Username。
- `ParseMentions(content, members)`（[`mentions.go`](../internal/bot/mentions.go)）：纯函数，无 I/O。把 `@<name>` 改写成 `<@bot_user_id>`，输出 `BotUserIDs` / `MentionedAccountIDs` / `Everyone` / `RewrittenContent`。原始 `<@123>` 直接拒，避免 agent 手写 ID 绕过成员检查。

实现：

- `bot/discord` ([`discord.go`](../internal/bot/discord/discord.go))：discordgo 包装。Intents 需要 `Guilds + GuildMembers + GuildMessages + MessageContent`（后两个是 Privileged，需要 Portal 勾选）。Send 路径统一走 `ChannelMessageSendComplex` + `AllowedMentions{Parse:[], Users:..., RepliedUser:...}`，默认拒所有 mention 除 `MentionAllowedUserIDs`。
- `bot/mock`：内存版，`InjectMessage` / `InjectEvent` 给测试注入。

### 4.2 `internal/connector`：Provider 生命周期 + 事件总线

- `Connector` 用一张 `map[accountID]*instance` 跟踪 Provider，instance 有三态：`connecting / online / disconnecting`。`Connect` 先在锁内占位（防 M3-P3-003 并发重复连接），慢的 `Provider.Connect()` 在锁外做。
- `Disconnect` 走 `online → disconnecting`（锁内）→ `Provider.Disconnect()`（锁外）→ 删 slot。失败走回滚（恢复 online，便于重试）。
- 每个 instance 配一个 `pumpDone chan struct{}`：`pump` goroutine 退出时 close，`Disconnect` 等它再删 slot，避免老 pump 残尾抹掉新订阅（M8-Q-P0-002 修）。
- `Subscribe / Unsubscribe` 给消费者发 buffered chan，**非阻塞**发送，慢消费者直接丢帧。当前消费者：`message.Ingester`（每账号一份）、`api/v1.DebugEvents`（每流一份）。

### 4.3 `internal/message`：inbound 入库

- 1 个 account online → 1 个 drain goroutine。`AttachAccount(id)` 在 `OnlineAccount` handler 里调；`DetachAccount(id)` 在 `OfflineAccount` 里调。
- 单个事件处理 = 单 `bundler.WithTx`：
  - `rooms.GetByDiscordChannelID` → 未找到的 channel 静默丢弃（未映射）。
  - `messages.CreateIgnoreConflict` → 拿到 `persistedID, inserted`。
  - **不论是否 inserted 都跑 mention 合并**：`MergeMentionEveryone(true)` + `MessageMentions.AddForMessage(resolved)`，避免 send 路径或 gateway echo 任一先到时丢字段（M9-P1 P2-1 finding 的修）。
  - 仅 `inserted=true` 时扇出 `message_states`（send 路径已经写过）+ `attachments`（占位行，downloader 后台取）。
- `EventChannelDeleted`（Discord 客户端手删 channel）→ 找到本地 room → archive 它 → 给该 room 全部 member publish state。
- 提交成功后调 `bus.PublishMany(memberIDs)`。**publish 在 tx 外**，避免回滚还触发重算（self-audit finding M-2）。

### 4.4 `internal/state`：状态聚合 + 长连接推送

- `Aggregator` 是纯读：给定 `accountID + version` 调一堆 repo 计数 / 列表后组装 `Snapshot`（8 维 + 公告 2 维 + health）。所有 list 维度 cap（mentions 50 / priority 50 / new_rooms 5 / recent 20 / announcements 20），但 totals 是真总数。
- `Bus` 持版本号（`atomic.Int64`）+ 200 ms 防抖 + 每账号最多 8 个 subscriber（`ResourceExhausted`）。
- `Subscribe()`：锁内做 `BuildNow` → 把首帧塞到新 chan → 注册 sub。这套顺序确保新 sub 看到的首帧 version ≤ 任何随后 fire 的 version（M5-P3-004 修）。
- `Publish(account)` → 装 / 重置 timer → 200 ms 后 `fire` → BuildNow + 非阻塞 send（慢消费者跳过——反正下一帧又是全量）。
- `Shutdown()` close 所有 sub chan、清 pending timer。

### 4.5 `internal/attachment`：后台下载

- 独立 goroutine，2 s tick；`processOnce` 拿 `ListPendingDownloads(50)`，每行：
  - `inBackoff` 跳过（指数退避，2 / 4 / 8 / 16 / 32 / 64 / 120 s，attempts ≥ 7 capped 120 s）。
  - `fetchOne`：`<cacheRoot>/<message-id>/<attachment-id>/<safe-filename>`，stream 到 tempfile → fsync → rename → `MarkDownloaded(local_path, sha256)`。
  - 校验 `Content-Length`，校验 ≤ MaxBytes（50 MiB 默认），`safeFilename` 把不在 `[A-Za-z0-9._-]` 的字符替成 `_`、去前导点、截到 80 字节、空则 `att-<id>`。
- 文件 perm `0o600`，目录 `0o700`，承袭 data-root 信任边界。

### 4.6 `internal/api`：HTTP-on-UDS

- `server.go` 用 chi 把路由挂上 → 中间件链：`Recover` + `Logger` + 公共 `/v1/healthz` + 带 `auth.Middleware.Handler` 的鉴权组 + 内部再分 `RequireAdmin` 子组。
- DTO 全在 `v1/types.go`；body cap = `MaxRequestBodyBytes = 1 MiB`（`DecodeJSON` 走 `http.MaxBytesReader` + `DisallowUnknownFields`）。
- 错误统一 `WriteError(w, err)`：从 `errcode.As` 抽 Code/Message/Details，HTTP status 走 `errcode.HTTPStatus`。`pkg/client` 反过来从 ErrorEnvelope 重建 `errcode.Error`。

#### 路由清单（按 router.go 出现顺序）

| 方法 | Path | 鉴权 | handler |
|---|---|---|---|
| GET | `/v1/healthz` | public | `Healthz` |
| GET | `/v1/whoami` | auth | `Whoami` |
| POST/GET/DELETE | `/v1/accounts[/{id}]` | admin | account CRUD + role |
| PATCH | `/v1/accounts/{id}` | admin | name/role；name 改动 + 已绑 token 时先 PATCH Discord |
| POST | `/v1/accounts/{id}/tokens` | admin | `CreateToken`（bcrypt 在 tx 外） |
| GET | `/v1/accounts/{id}/tokens` | admin | metadata only |
| DELETE | `/v1/tokens/{id}` | admin | `RevokeToken` |
| POST | `/v1/accounts/{id}/discord` | admin | `SetDiscord`（Prober → AES → 同步 name） |
| POST | `/v1/accounts/{id}/online` / `/offline` | admin | 生命周期 |
| GET | `/v1/accounts/{id}/status` | admin | status + identity |
| GET | `/v1/audit` | admin | filter by account/since/limit |
| POST | `/v1/debug/send` / GET `/v1/debug/events` | admin | provider 直发 / 事件流 |
| POST/PATCH/DELETE | `/v1/rooms[/{id}][/archive]` | admin | room CRUD |
| POST/DELETE | `/v1/rooms/{id}/members[/{aid}]` | admin | invite/kick |
| POST | `/v1/system/announcements` | admin | system announcement |
| GET | `/v1/rooms[/{id}/members]` | auth | list/show |
| PATCH | `/v1/memberships/{room_id}` | auth | self subscribe/unsubscribe |
| POST | `/v1/rooms/{id}/messages` | auth | send |
| POST | `/v1/rooms/{id}/read` | auth | M9-P2 read+mark+context |
| POST/GET | `/v1/rooms/{id}/announcement` | auth | 任成员可发 |
| POST | `/v1/announcements/{id}/read` | auth | 任成员可 ack |
| GET | `/v1/system/announcements` | auth | list + 每行 read 状态 |
| POST | `/v1/system/announcements/{id}/read` | auth | ack |
| GET | `/v1/state` | auth | one-shot Snapshot |
| GET | `/v1/state/watch` | auth | NDJSON 长连接 |

> 旧接口 `GET /v1/rooms/{id}/messages` / `POST /v1/messages/{id}/read` / `POST /v1/messages/{id}/reply-ack` 在 M9-P2 已删——`POST /v1/rooms/{id}/read` 一把替代。

### 4.7 `pkg/client`：公共 Go 客户端

- 通过 UDS 拨号，包装每个 endpoint 成一个 typed method（`CreateAccount` / `ReadRoom` / `WatchState` / ...）。
- 复用 `internal/api/v1.*Request/Response` DTO（同一 module 内部）。
- 错误从 daemon 的 ErrorEnvelope 重建 `errcode.Error`，所以 CLI 的 `cliutil.PrintAndExit` 能用同样的 exit code 映射。
- 长流端点（`StreamEvents` / `WatchState`）建独立 `http.Client` 不带 timeout，依赖请求 context 取消。

### 4.8 `cmd/agentchat/cmds`：CLI 命令树

每个文件 = 一个 / 一组 subcommand：

| 文件 | verbs |
|---|---|
| `root.go` | 全局 flag (`--token` / `--socket` / `--json`)；token 解析（flag > env > `cli.toml`） |
| `output.go` | TTY 检测 + 表格 / JSON 渲染 helper |
| `version.go` | `version` |
| `whoami.go` | `whoami` |
| `state.go` | `state`, `watch state` |
| `send.go` | `send <room> [text]` `--reply --priority --file --attach` |
| `read.go` | `read <room>` `--before --limit` |
| `room.go` | `room create/list/show/rename/archive/delete/invite/kick/members/subscribe/unsubscribe` |
| `announce.go` | `room announce`, `room announce-show`, `ack-announcement`, `system-announcements`, `ack-system`, `admin system-announce` |
| `admin.go` + `admin_*.go` | admin 子树：`account / token / audit` |
| `debug.go` | `debug send / debug events` |

`outputJSON()` 决定输出形态：`--json` 或 stdout 非 TTY → JSON；否则 table。

---

## 5. 关键不变量与设计选择

以下是 review / 重构时**不能擅自改动**的几条契约：

1. **每个 mutation 与其 audit row 同事务**（M2-P3-012）。任何 API handler 加 mutation 必须走 `bundler.WithTx` 并用 `RecordVia(b.Audit, ...)`。否则审计可能比业务先一刻钟。
2. **bcrypt / Discord REST 等慢调用必须在 tx 外**。SQLite 单写连接（`MaxOpenConns=1`）；tx 里跑 bcrypt 会把别的写操作全堵住（M2-P3-014 fix）。`CreateToken`、`UpdateAccount` 的 prober rename、send 路径的 `Provider.SendMessage` 都遵循这条。
3. **Discord 是身份权威**。`set-discord` 拿到 `Probe()` 的 `(user_id, username)` 后强制写回 `accounts.bot_user_id` 与 `accounts.name`；rename 走 `Prober.Rename` 先（限流 → `UNAVAILABLE`），本地再写。
4. **state.Bus.Publish 必须在 tx commit 之后**。`message.Ingester` 把 `notify` 存到闭包外，commit 成功才 `PublishMany`，否则回滚事务也会触发重算（self-audit Finding M-2 修）。
5. **`messages.discord_msg_id` UNIQUE 是 send / ingest 的会合点**。任何处理消息的新路径必须接受"另一边已经 INSERT 了"的情况——用 `CreateIgnoreConflict + ApplySendMetadata + MergeMentionEveryone + AddForMessage` 的幂等组合。
6. **`message_states` 给每个成员都写，订阅与否只影响通知**（M4-P3-005）。统计维度（`Count*ForSubscribed`）才按 subscribed 过滤。
7. **每事件单元的失败模式被 errcode 收敛**。Provider 把 Discord 404 → `NotFound`，403 → `PermDenied`，其它 → `Unavailable`。Provider 的 401 → `AuthInvalid`。
8. **socket / data-root / master.key / attachments 全 0o600 / 0o700**。`config.EnsureDataRoot` 每次启动重新收紧；`master_key.go` 每次读 master.key 也 chmod 一次。`safeFilename` 不让 CDN-supplied filename 绕出目录。
9. **Stale online 状态会在 daemon 启动 / 关闭时强制 reconcile**。`reconcileStaleOnlineLifecycles` 在 boot 后立即跑（SIGKILL / panic / 断电后清理）和在 graceful shutdown 时跑（SIGINT/SIGTERM 之后立刻反映 DB 状态）。actor 用 `"system"`，action `account.lifecycle_reconcile`。
10. **`mock.Prober` 与 `discord.Prober` 行为对称**。Mock 默认按 hint.Username 返回 identity，所以测试默认走 happy path；要测 CONFLICT / force-rename，用 `SetIdentity(token, id)` 显式 stash 另一个 identity。

---

## 6. 并发模型

- **SQLite 串行化**：`MaxOpenConns=1`，所有写者走单连接，事务用 `_txlock=immediate`。读写并发依赖 WAL，但 dirty read 不会发生。
- **Connector mutex**：保护 `instances` + `subs` map；pump 在锁内 select-send 给订阅者（非阻塞），保证发送和 close 同序（M3-P3-004）。
- **Bus mutex**：`buildNowLocked / Subscribe / fire` 都在锁内；`Publish` 设 timer 在锁内（防 timer pending 计入两次）。
- **Ingester 锁**：自有 `attached` map 防并发 attach；run goroutine 退出时 delete 自己。
- **CLI 端不持锁**：每个命令一次性 round-trip。
- **附件下载**：单 goroutine 串行下载（`pollInterval=2s`），失败重试间隔自管。

---

## 7. Token / 加密

- **API token**：`agch_<32hex-uuidv7>_<43base64url>`。`tokens.token_hash = bcrypt(secret, cost=12)`。Verify 用 raw 拆出 id → repo.Get(id) → bcrypt compare。**id 公开，secret 不公开**，泄漏 id 不能反推 secret。`PrepareToken` 先做 bcrypt（慢，~150 ms）再 `PersistTokenVia`（INSERT，快）；test build 把 `APITokenCost` 改 `bcrypt.MinCost` 跑得快。
- **Discord bot token**：AES-256-GCM(`master.key`) 加密成 `accounts.bot_token_enc`。`master.key` 32 字节随机，存 `<data-root>/master.key`，0o600，每次启动再 chmod。`AESGCMDecrypt` 失败按 `AuthInvalid` 返回（被篡改 / wrong key）。

---

## 8. 错误码契约

详见 [`internal/errcode/errcode.go`](../internal/errcode/errcode.go)。常用：

| Code | HTTP | exit | 触发举例 |
|---|---|---|---|
| `AUTH_MISSING` | 401 | 10 | Authorization header 没填 |
| `AUTH_INVALID` | 401 | 11 | token id 不存在 / bcrypt 不匹配 / Discord 401 |
| `AUTH_REVOKED` | 401 | 12 | token 已 `revoke` |
| `PERM_DENIED` | 403 | 13 | user 调 admin / 非成员 read 别人房间 |
| `NOT_FOUND` | 404 | 20 | room / message / announcement id 不存在 |
| `CONFLICT` | 409 | 21 | 双开 daemon、name 撞、send 进 archived room、`set-discord` 撞名 |
| `INVALID_ARGUMENT` | 400 | 22 | 参数非法、未配 guild_id 时建 room、正文有 raw `<@123>` |
| `PAYLOAD_TOO_LARGE` | 413 | 22 | request body > 1 MiB |
| `ATTACHMENT_TOO_LARGE` | 413 | 22 | 单附件 > 10 MB（Discord free-tier cap） |
| `RESOURCE_EXHAUSTED` | 429 | 21 | 同账号已 8 条 watch state |
| `UNAVAILABLE` | 503 | 51 | Discord 5xx / 网络 / bot rename 限流 |
| `INTERNAL` | 500 | 50 | daemon 内部 bug / panic |

`ExitCode` 是 CLI 的 stable contract——脚本可以 branch。

---

## 9. CLI 输出契约

- 默认：TTY → 人类视图（表格 / 多行 Key-Value），pipe → JSON（[`output.go:outputJSON`](../cmd/agentchat/cmds/output.go)）。
- `--json` 强制 JSON。
- 错误：默认 `Error [CODE]: msg\nCaused by: ...`；`--json` 时 stderr 是 `{"error":{...}}`（[`internal/cliutil/cliutil.go`](../internal/cliutil/cliutil.go)）。
- `watch state` / `debug events` 严格 NDJSON（一行一帧），无心跳；下游 `jq` 需要 `--unbuffered` 否则块缓冲看起来"卡住"。

---

## 10. 与历史文档的差异

> 旧 `docs/00-overview.md` 到 `docs/06-cli-redesign.md`（现位于 [`archive/`](./archive/)）描述的是**计划态** + 各 milestone 时刻的瞬时实现。本系列描述的是 **当前主干代码**。差异速记：

- `mention_all` / `requires_ack` / `replied_at` 三列已被 M9-P2 migration 0006 DROP；现在 `mention_everyone` + `message_mentions` 表是事实。
- 旧 `history` / `read <msg-id>` / `reply-ack` CLI 不再存在；统一走 `read <room>` + 默认行为标读。
- `set-discord` 现在 **必须** 通过 `IdentityProber` 校验 token、同步 name；test 走 mock prober。
- `account.rename` 改动名字 + 已绑 bot token 时，daemon 先调 Discord PATCH /users/@me 再写本地（命中限流 → `UNAVAILABLE`）。
- daemon boot / shutdown 都跑 `reconcileStaleOnlineLifecycles`（旧文档没这个）。
- `internal/bot.IdentityProber` 是 M9-P2 新引入的 seam。
- watch state 单账号 ≤ 8 concurrent subscriber（旧文档未提；M8-S-P2-008 加固）。

如果你 stumble 到一个 doc 提到上述特性的旧形式——它是 archive。
