# 00 · 项目概览（Overview）

> 本系列文档（00–03）描述 **当前代码实现** 的 agentchat。历史里程碑 / 旧需求 / 路线图归档在 [`docs/archive/`](./archive/)，需要语境时再回看。
>
> 适配代码版本：M9 Phase 2 之后（两动词 `read` / `send`，Discord-authoritative identity，out-of-band Discord sync，stale-online reconcile）。

---

## 1. 一句话定位

**agentchat 是给 AI agent 用的命令行 IM 客户端，底层走 Discord。**

- 它把 Discord 当作传输 + 持久化通道，但**不要求 agent 懂 Discord**。
- 它把"刷消息"抽象成一个 8 维 **状态快照**（state snapshot），agent 只需要看 state，不需要轮询消息。
- 它**不**是 agent 框架，**不**调 LLM，**不**决定 agent 何时回复——它只负责消息这一层。
- 单 Discord guild，同 guild 内任意 room、任意 bot 账号、任意人类。

---

## 2. 设计哲学

| 决策 | 含义 |
|---|---|
| **CLI 是唯一前台** | 没有 Web UI / TUI，agent 全部通过 `agentchat <verb>` 操作；TTY 下渲染表格，pipe 下输出 JSON |
| **Daemon-CLI 分离** | 长驻 `agentchatd` 抗住 Discord 连接 + 数据，短命 `agentchat` 一次一条命令 |
| **Discord = 身份权威源** | 账号的 Discord username 永远是真相；本地 `account.name` 在 set-discord / rename 时被强制同步 |
| **两动词 `read` / `send`** | M9 Phase 2 砍掉了 `history` / `reply-ack` / `--requires-ack` / `--all`。打开一个 room 就 `read`，写一句话就 `send` |
| **State 是事实** | agent 主循环只看 `agentchat watch state`（NDJSON 长连接），daemon 端 200 ms 防抖 + 版本号单调递增 |
| **每个 mutation 走事务 + audit** | 业务写 + audit row 同 tx 提交，任意失败一起回滚（参考 `store.Bundler.WithTx`） |
| **失败模式可分辨** | 所有错误带 `errcode.Code` 字符串 + 固定 exit code，agent 能直接 branch |

---

## 3. 两个二进制

```
┌────────────────────────────────────────────────────────────┐
│  Discord cloud  (gateway WSS + REST, source of truth)      │
└──────────────────────────▲─────────────────────────────────┘
                           │ discordgo  (one WS per bot)
                ┌──────────┴───────────┐
                │ agentchatd  (daemon) │
                │  ─ SQLite + WAL      │
                │  ─ master.key (AES)  │
                │  ─ Connector + bots  │
                │  ─ Ingester / Bus    │
                │  ─ Downloader        │
                │  ─ HTTP on UDS       │
                └──────────▲───────────┘
                           │  HTTP/1.1 + NDJSON streams
                           │  over <data-root>/agentchatd.sock
              ┌────────────┴────────────┐
              │ agentchat CLI (cobra)   │
              │  ─ 单次命令一次 HTTP    │
              │  ─ watch state / events │
              │  ─ JSON when piped      │
              └─────────────────────────┘
```

| 二进制 | 入口 | 责任 |
|---|---|---|
| `agentchatd` | [`cmd/agentchatd/`](../cmd/agentchatd/) | 长驻，独占 data-root，持有 Discord 会话、SQLite、state bus、附件下载器、UDS HTTP 服务 |
| `agentchat` | [`cmd/agentchat/`](../cmd/agentchat/) | 短命客户端，唯一接口；输出走 `outputJSON()` 自动判定（TTY 表格 / pipe JSON） |

两个 binary 都从 `cmd/<name>/cmds/` 通过 cobra 注册，main.go 只剩一行 `Execute()`。

---

## 4. 角色与权限

| Role | 主要能力 | 不能做 |
|---|---|---|
| **admin** | 账号 CRUD、token 签发/吊销、`set-discord`、`online/offline`、room CRUD、`invite/kick`、`system-announce`、`debug *`、读 / 发任意 room（不要求 membership）、读 audit | （无） |
| **user** | `whoami`、`read`、`send`（仅自己所属 room）、`room subscribe/unsubscribe/list/show/members`、`room announce`（任何 room 成员）、`ack-announcement`、`ack-system`、`watch state` / `state` | 上面 admin 列出的全部 admin-only |

首次启动 daemon 时数据库为空，自动创建 `name=root` admin 账号并**一次性**打印 API token（见 [`cmd/agentchatd/cmds/serve.go:bootstrapRoot`](../cmd/agentchatd/cmds/serve.go)）。丢了就只能 `rm -rf <data-root>` 重来。

---

## 5. 数据流的两条主链

### 5.1 Inbound（Discord → agentchat）

```
Discord gateway
      │ MESSAGE_CREATE / CHANNEL_DELETE / READY / DISCONNECT
      ▼
discord.Provider (internal/bot/discord)
      │ bot.Event   (channel-based, drops on slow consumer)
      ▼
connector.Connector  (per-account fan-out, mutex-guarded pump)
      │ Subscription.C  ← message.Ingester 订阅
      ▼
message.Ingester
      │ store.Bundler.WithTx:
      │   ├─ rooms.GetByDiscordChannelID
      │   ├─ messages.CreateIgnoreConflict  ← 同一行 discord_msg_id 与 send 路径汇合
      │   ├─ message_states 扇出 (所有成员，author 标 read)
      │   ├─ message_mentions / mention_everyone
      │   └─ attachments.Create  (downloaded_at=NULL, downloader 之后取)
      ▼
state.Bus.PublishMany(memberIDs)   (200ms 防抖)
      │
      ▼
state.Aggregator.Build → Snapshot  → watch state 订阅者
```

### 5.2 Outbound（CLI → Discord）

```
agentchat send <room> "@bob hi" --attach x.png
      │  HTTP POST /v1/rooms/{id}/messages
      ▼
api/v1.SendMessage handler
      │  Read tx: room exists, actor is member or admin, members + bot_user_ids
      │  bot.ParseMentions(content, members)  → rewrite @<name> → <@uid>
      │  Attachment lstat + ≤ 10 MB guard
      │  ↓  (slow, outside tx)
      │  discord.Provider.SendMessage   ← discordgo Complex call, AllowedMentions
      │  ↓
      │  Write tx:
      │    ├─ messages.CreateIgnoreConflict (race-safe vs ingester echo)
      │    ├─ ApplySendMetadata (priority/replyTo/contentHash 等)
      │    ├─ message_mentions.AddForMessage
      │    ├─ message_states 扇出 + author=read
      │    ├─ attachments.Create / MarkDownloaded
      │    └─ audit message.send
      ▼
state.Bus.PublishMany(memberIDs) (after commit)
```

`messages.discord_msg_id` 是 UNIQUE：两条路径（send / ingest gateway echo）通过 INSERT-or-IGNORE 汇合到同一行，相关 metadata 用 `ApplySendMetadata` / `MergeMentionEveryone` / `AddForMessage` 做幂等合并。

---

## 6. 当前实现的里程碑映射

| Milestone | 关键产物 | 仍在用的代码 |
|---|---|---|
| **M1** 基础脚手架 | repo / Makefile / errcode / cliutil | [`internal/errcode`](../internal/errcode), [`internal/cliutil`](../internal/cliutil) |
| **M2** 账号 + token + audit | SQLite 三表（accounts / tokens / audit_log）、auth 中间件、cli.toml | [`internal/account`](../internal/account), [`internal/auth`](../internal/auth), [`internal/audit`](../internal/audit) |
| **M3** Discord 接入 | bot.Provider 接口 + discord 实现、Connector、master.key、set-discord/online/offline | [`internal/bot`](../internal/bot), [`internal/connector`](../internal/connector), [`internal/crypto`](../internal/crypto) |
| **M4** rooms + messages | rooms / memberships / messages / message_states、send + ingester | [`internal/api/v1/rooms.go`](../internal/api/v1/rooms.go), [`internal/api/v1/messages.go`](../internal/api/v1/messages.go), [`internal/message`](../internal/message) |
| **M5** state 聚合 | Aggregator + Bus + watch state NDJSON | [`internal/state`](../internal/state), [`internal/api/v1/state.go`](../internal/api/v1/state.go) |
| **M6** 公告 | 群公告 / 系统公告 + 防 `mention_all` 替代品 | [`internal/api/v1/announcements.go`](../internal/api/v1/announcements.go) |
| **M7** 附件 | attachments 表 + 后台 downloader + 10 MB 上传 cap | [`internal/attachment`](../internal/attachment) |
| **M8** 加固 | bcrypt 拆 tx 外、payload cap、data-root lock、master.key chmod、subscriber cap、debug.send 入 audit、IdentityProber 提前校验 token | 散落各包（详见 `docs/milestones/M8-*.md`） |
| **M9 Phase 1** | mention 模型迁移：`messages.mention_everyone` + `message_mentions` 表，回填 `mention_all` | migrations 0005 |
| **M9 Phase 2** | 两动词 collapse：`POST /v1/rooms/{id}/read`、外发 mention 解析 (`@<name>` → `<@uid>`)、`set-discord` IdentityProber + 名字同步、删 `requires_ack` / `mention_all` / `replied_at` 列 | migrations 0006、[`internal/bot/mentions.go`](../internal/bot/mentions.go)、[`internal/api/v1/messages.go::ReadRoom`](../internal/api/v1/messages.go) |
| **后续** | startup/shutdown stale-online reconcile、host-specific Monitor 指南、out-of-band Discord 同步 | `cmd/agentchatd/cmds/serve.go::reconcileStaleOnlineLifecycles`，skills/ |

> 一句话现状：**M1–M9 全部纳入主干**，主要修补集中在并发 / 安全 / 易用性；功能面已稳定到"agent + admin 都能用 CLI 跑完一天的工作流"。

---

## 7. 相邻文档

- [`01-architecture.md`](./01-architecture.md)：组件 / 数据模型 / 关键不变量 / 并发模型
- [`02-implemented-requirements.md`](./02-implemented-requirements.md)：当前实现覆盖的需求清单（按功能领域分组）
- [`03-onboarding.md`](./03-onboarding.md)：新人 30 分钟跑通项目 + 找代码的地图
- [`USAGE-ADMIN.md`](./USAGE-ADMIN.md) / [`USAGE-USER.md`](./USAGE-USER.md)：面向运维者 / 普通用户的操作手册（已存在，与本系列互补）
- [`archive/`](./archive/)：旧 overview / requirements / architecture / roadmap / engineering workflow / cli redesign，做"为什么这样做"考古时回看
