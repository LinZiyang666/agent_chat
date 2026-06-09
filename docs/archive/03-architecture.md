# Agent Chat — 架构设计 v0.1（讨论稿）

> 状态：架构师起草的初稿，**等用户对每个决策点表态后定稿**。
> 基线：`02-requirements-final.md` v1.0。
> 风格：每个决策点给出**推荐方案 + 理由 + 备选 + 取舍**，方便用户判断、反驳、修改。

---

## 0. 从需求推导出的硬约束

在做任何选型之前，先列出**需求决定我们必须做的事**：

| # | 约束 | 来源 |
|---|---|---|
| H1 | 必须有**常驻进程**维护 Discord 长连接、本地缓存、状态聚合 | C16 状态界面要"监听"；C20 一致性；C22 离线补消息 |
| H2 | 必须支持**多账号并发**（多 CLI 进程各自一个账号同时跑） | C21 |
| H3 | 必须有**本地数据库**（缓存消息、状态、附件索引） | C20 / C18 |
| H4 | CLI 必须同时支持**机器可读（JSON）+ 人类可读（TTY）** 两种输出 | 1.1 项目定位 |
| H5 | 必须支持**流式订阅状态界面**（不是一次性查询） | C16 主状态界面 |
| H6 | 每个账号必须有**独立的 Discord 身份**（能被 @、被踢、加入老群看历史） | §3.2/§4 全部三态语义 |

H1+H2 直接决定**必须有一个 daemon 进程**，CLI 是它的客户端——这是整个架构的根。

---

## 1. 进程拓扑

### 推荐：单 daemon + 短命 CLI 客户端

```
┌─────────────────────────────────────────────────────────────┐
│  Discord  (云端权威数据源)                                    │
└─────────────────────────────▲───────────────────────────────┘
                              │ discordgo (WS Gateway + REST)
              ┌───────────────┴────────────────┐
              │                                │
              │      agentchatd  (daemon)       │
              │  ┌──────────────────────────┐   │
              │  │ Discord Bot 连接池         │  │  ← 每账号一条
              │  ├──────────────────────────┤   │
              │  │ 业务层 (account/room/msg) │  │
              │  ├──────────────────────────┤   │
              │  │ 状态聚合引擎              │  │
              │  ├──────────────────────────┤   │
              │  │ 附件下载/索引器           │  │
              │  ├──────────────────────────┤   │
              │  │ 本地存储 (SQLite + 文件)  │  │
              │  ├──────────────────────────┤   │
              │  │ HTTP API (Unix socket)   │  │
              │  └────────────▲─────────────┘   │
              └───────────────┼────────────────┘
                              │ HTTP / NDJSON
              ┌───────────────┴───────────────┐
              │                               │
              │   agentchat  CLI (短命进程)    │
              │   - 每次执行一条命令           │
              │   - 解析参数、格式化输出       │
              │   - 流式订阅时保持连接         │
              └───────────────────────────────┘
                              ▲
                              │  N 个 CLI 进程并发
                              │  各自绑一个账号 (env: AGENTCHAT_TOKEN)
```

### 为什么不是"用户最初提的三层独立进程"？

用户原话："discord → discord bot → agent gateway"。我把这理解为**逻辑分层**而非**物理进程拆分**。在单 daemon 内：

```
internal/
  bot/        ←——— "Discord Bot 层"，封装 discordgo，对上提供归一化事件
  api/        + 业务包 ←——— "Agent Gateway 层"
```

如果将来需要扩展到"bot 跑在云端、gateway 跑在本机"，再拆进程不迟。**早拆只增加 IPC 复杂度，不带来收益**。

### 备选 & 取舍

| 方案 | 优点 | 缺点 |
|---|---|---|
| **A 单 daemon (推荐)** | 简单、状态共享、性能好 | 单点；故障影响所有账号 |
| B 多 daemon (每账号一个) | 故障隔离 | N 倍资源；多 DB 协调难；多 socket |
| C 无 daemon (CLI 直连 Discord) | 极简 | 无法做流式订阅；每次启动握手 Discord 太慢；多 CLI 并发抢连接 |

**待用户确认**：是否同意单 daemon 方案？

---

## 2. CLI ↔ daemon 的 IPC 协议

### 推荐：HTTP/1.1 over Unix Domain Socket（NDJSON for streaming）

- **传输层**：Unix Domain Socket（`~/.agentchat/agentchatd.sock`）
  - Linux/Mac/WSL 通用
  - 自带文件系统级权限控制（chmod 600）
- **协议层**：标准 HTTP/1.1
  - 一次性命令：普通请求/响应（JSON body）
  - 流式订阅：响应保持打开，**NDJSON**（每行一个 JSON 对象）
- **认证**：HTTP Header `Authorization: Bearer <agentchat-token>`

### 为什么是 HTTP 而不是 gRPC？

| 维度 | HTTP+JSON | gRPC |
|---|---|---|
| 调试 | `curl` 一行 | 需 grpcurl / 专用工具 |
| Agent 友好 | 任何能 HTTP 的语言都能用 | 需 proto + codegen |
| 流式 | NDJSON / SSE | 原生 stream |
| 性能 | 本地 socket 够快 | 略快 (本地差异可忽略) |
| 工具生态 | 全宇宙 | 受限 |

**Agent 用户友好优先**——HTTP+JSON 让任何 agent 都能直接调，包括 `curl | jq`。

### 备选

- **gRPC**：未来若有强类型契约需求可上，但目前 over-engineered
- **stdin/stdout RPC**：多 CLI 并发难，流式订阅困难——pass
- **TCP 端口**：跨用户安全管控麻烦，本机没必要

**待用户确认**：是否同意 HTTP over Unix Socket？

---

## 3. Discord Bot 实现方式（最关键的决策）⚠️

### 需求约束回顾

每个账号都必须能：
- 被 @ 提及（user mention）
- 被踢出群 / 被邀请入群
- 加入老群后看历史
- 有自己的头像 + 名字 + 权限

### 三种候选

| 方案 | 描述 | 行不行 |
|---|---|---|
| A | **每账号一个独立 Discord Application + Bot Token** | ✅ 可行 |
| B | **一个 Master Bot + Webhook 伪装身份** | ❌ Webhook 不能"加群"也不能被 @，不满足语义 |
| C | **混合（master 监听 + webhook 发言）** | ❌ 同 B，缺 user 级身份 |

→ **唯一可行的是 A**。

### A 方案的代价（用户必须知道）

1. **每加一个 agent 账号，用户要去 [Discord Developer Portal](https://discord.com/developers/applications) 手动创建一个 Application、生成 Bot Token、把 Bot 加入服务器**。
2. **Discord 对个人开发者账号的 Application 数量有限制**：
   - 标准限制是每个 Discord 账号最多 **10 个 application**（历史上是 10–20，可申请提升）
   - 超过需要走 Discord 支持申诉

### 关键问题需要你回答 ⚠️

> **Q3.1**：你预期同时活跃的 agent 账号大概多少个？
>   - (a) ≤ 5 个：完全没问题
>   - (b) 5–10 个：紧贴限制，但能跑
>   - (c) > 10 个：方案 A 不够用，需要研究"团队账号 / 多 Discord 账号 / 申请提额"等绕路方法
>   - (d) 还不确定，但**先按 ≤ 10 设计**

> **Q3.2**：admin 通过 CLI **创建账号** 时，是否需要 admin 手动粘贴一个 Bot Token？
>   - (a) 是：admin 在 Discord 创建好 application、拿 token、粘进来 → 我们绑定
>   - (b) 自动化：通过 Discord OAuth + Team API 自动 provision（**复杂得多**，要先有 team，且 Discord 不太允许全自动）
>   - 推荐 (a)，把 Discord-side 的事留给运维。

---

## 4. 本地存储

### 推荐：SQLite (modernc.org/sqlite, 纯 Go 无 cgo)

- 单机、单 daemon、不需要分布式 ⇒ SQLite 完美匹配
- 内置事务、外键、**FTS5 全文搜索**（搜消息历史时香）
- modernc.org/sqlite 是纯 Go 实现，**不需要 cgo**，交叉编译方便
- 备份就是 copy 一个 .db 文件

### 文件布局

```
~/.agentchat/
├── config.toml             # 配置（daemon 端）
├── agentchatd.sock         # IPC socket
├── agentchatd.db           # SQLite 主库（账号/群/消息/状态）
├── agentchatd.log          # 日志
└── attachments/
    └── <room-id>/
        └── <message-id>/
            └── <attachment-name>   # 附件本地副本
```

### Schema 草图（仅核心表）

```sql
accounts(id, name, role, discord_bot_token_enc, lifecycle_state, created_at, ...)
rooms(id, discord_channel_id, name, current_announcement_id, archived, ...)
memberships(account_id, room_id, subscribed, joined_at, ...)     -- 三态由 (membership 存在 + subscribed) 表达
messages(id, room_id, author_account_id, discord_msg_id, content, reply_to, requires_ack, priority, created_at, content_hash, ...)
message_states(message_id, account_id, read, replied, ...)        -- per-account 状态
announcements(id, room_id, content, version, created_at, ...)
announcement_reads(announcement_id, account_id, read_at)         -- 必读语义
attachments(id, message_id, filename, size, mime, local_path, discord_url, ...)
reactions(message_id, account_id, emoji, ...)
system_announcements(id, content, created_at, ...)
system_announcement_reads(sys_announcement_id, account_id, ...)
tokens(id, account_id, token_hash, created_at, revoked_at)        -- 本系统颁发给 CLI 的 API token
```

**待用户确认**：是否同意 SQLite？

---

## 5. 状态界面（State View）的传输

### 推荐：NDJSON over HTTP（长响应保持打开）

CLI 端体验：
```bash
$ agentchat watch state                  # 持续吐 JSON line 直到 Ctrl-C
$ agentchat watch state --json | jq      # agent 用
$ agentchat state                        # 当前快照，一次返回
```

服务端实现：
- daemon 内部状态聚合引擎维护**每账号一个 state snapshot**
- 任何业务事件（新消息、reaction、群变更）→ 触发 snapshot 重算 → 推送给所有正在 watch 的连接
- 支持 cursor，断线重连不丢

### 备选

- WebSocket：可，但 NDJSON 更简单 + curl 兼容
- SSE (Server-Sent Events)：可，本质和 NDJSON 一样
- 轮询：差，状态界面要"实时"

**待用户确认**：是否同意 NDJSON over HTTP？

---

## 6. 鉴权（CLI ↔ daemon）

### 推荐：daemon 颁发的 API Token，存 env

- **本系统颁发的 token ≠ Discord bot token**（后者是 daemon 内部用的、不暴露给 CLI）
- admin 通过 CLI 创建账号时，daemon 生成一条 token 给 admin，admin 把 token 写到 agent 的环境变量里
- CLI 优先级：
  1. `--token <s>` 参数（仅 debug 用）
  2. `AGENTCHAT_TOKEN` env var（推荐，agent 通用）
  3. `~/.agentchat/cli.toml` 文件（人类用，方便切账号）
- 存储：daemon 端只存 token 的 **bcrypt hash**，原始值只在创建时返回一次
- 撤销：`agentchat admin token revoke <id>`（admin only）

**待用户确认**：是否同意此鉴权方案？

---

## 7. 技术栈

| 用途 | 推荐 | 备注 |
|---|---|---|
| Go 版本 | 1.22+ | std slog、std maps 等够用 |
| CLI 框架 | **spf13/cobra** | 事实标准 |
| 配置 | env + TOML（toml-lang/toml-go 或 BurntSushi/toml） | 不上 viper，太重 |
| 日志 | **log/slog**（标准库） | 结构化、生态成熟 |
| HTTP server | `net/http` + **go-chi/chi/v5** | chi 路由薄、好理解 |
| SQLite | **modernc.org/sqlite** | 纯 Go，无 cgo |
| Discord | **bwmarrin/discordgo** | 事实标准 |
| 密码哈希 | `golang.org/x/crypto/bcrypt` | 给 API token 用 |
| 加密存储 (bot token) | **AES-GCM** + 主密钥从 env / keyring | 不要明文存 bot token |
| 测试 | std `testing` + **stretchr/testify**（断言糖） | 不上重型框架 |

**待用户确认**：技术栈是否需要替换任何一项？

---

## 8. 模块划分（仓库目录）

```
agentchat/
├── cmd/
│   ├── agentchat/             # CLI 入口
│   └── agentchatd/            # daemon 入口
├── internal/
│   ├── api/                   # daemon HTTP API（CLI 调用接口）
│   ├── bot/                   # Discord Bot 抽象（封装 discordgo，事件归一化）
│   ├── account/               # 账号 CRUD + 生命周期
│   ├── room/                  # 群 CRUD + 成员关系
│   ├── message/               # 消息收发、状态、reply 链
│   ├── attachment/            # 附件下载、本地索引
│   ├── announcement/          # 三种公告
│   ├── state/                 # 状态界面聚合引擎
│   ├── auth/                  # API token 鉴权
│   ├── store/                 # SQLite 存储层（DAO）
│   ├── config/                # 配置加载
│   ├── errcode/               # 错误码定义（C24 (A) 要求）
│   └── health/                # 系统健康栏（C24 (B) 要求）
├── pkg/
│   └── client/                # CLI 端 HTTP client（pkg 因为可能给第三方 agent SDK 用）
├── docs/
└── go.mod
```

**待用户确认**：模块划分是否需要调整？

---

## 9. 决策点清单（请逐个表态）

| # | 决策点 | 推荐 | 你的态度 |
|---|---|---|---|
| D1 | 进程拓扑：单 daemon + CLI | ✅ | ✅ **已定：A 单 daemon** |
| D2 | IPC：HTTP over Unix Socket + NDJSON | ✅ | ✅ **已定**：HTTP/1.1 over Unix Socket + NDJSON 流 + `/v1/` 前缀 |
| D3 | Discord Bot：每账号一个独立 Application | ✅（唯一可行） | ✅ **已定：Discord，每账号一个 application** |
| D3.1 | 预期同时活跃 agent 数量（决定限制是否够） | (待你回答) | ✅ **已定：≤ 10**（接受 Discord 个人开发者限制） |
| D3.2 | Bot Token 由 admin 手动粘贴 | ✅ | ✅ **已定：手动粘贴**（Discord 强制） |
| **D3.3 (新)** | **Bot 层必须做成可换平台的抽象** | — | ✅ **已定：必须**——业务层只依赖 `bot.Provider` 接口，Discord 实现放 `internal/bot/discord/`；未来换 Matrix/Slack 只换实现 |
| D4 | 本地存储：SQLite (modernc.org/sqlite) | ✅ | ✅ **已定**：modernc/sqlite + `~/.agentchat/` (env 可覆盖) + 附件存文件系统 |
| D5 | 状态界面：NDJSON over HTTP 长响应 | ✅ | ✅ **已定**：Snapshot only + 200ms debounce + daemon 端聚合 + version cursor 重连。**无事件期 daemon 静默**（不发心跳），保证 agent 的 Monitor/background shell 不被无效唤醒。 |
| D6 | 鉴权：daemon 颁发 API Token + env var | ✅ | ✅ **已定**：token 永久 + 一账号多 token + 首次启动自动建 root admin 打印 token + audit_log 表记 admin 操作 |
| D7 | 技术栈：cobra / chi / discordgo / slog / sqlite | ✅ | ✅ **已定**：全部按推荐；module path = `github.com/LinZiyang666/agentchat` |
| D8 | 模块划分：cmd / internal / pkg 三段式 | ✅ | ✅ **已定**：cmd/agentchat/cmds 分文件 + migrations 在 internal/store/ 下 + pkg/client/ 第一版就做 + 单 go.mod 不用 workspace |

---

## 10. 一次性 vs 分次确认

你可以：
- **快速通过**：回复 "D1–D8 全部 ✅，D3.1 我答 X"（推荐，推进最快）
- **逐个讨论**：从 D1 开始，每个问题展开聊，我详细解释取舍
- **挑剔某些**：明确告诉我"D2 我不想用 HTTP，原因是 ..."

哪种方式你舒服，我就走哪条。

---

## 11. 实施纪律（约束）

**11.1 使用任何外部库前必须查最新官方文档。**

写到 `import "github.com/xxx/yyy"` 之前，必须 WebFetch / WebSearch 该库的官方文档（GitHub README / godoc / 官网），确认当前版本的 API、参数、签名、是否有 deprecation。

理由：模型训练数据可能过时——库经常 v2/v3 重构、参数改名、行为变更。凭记忆写的代码常常看起来对、跑不通，反而耽误时间。

适用范围：**所有非 std 包**，包括"事实标准"的（cobra、chi、discordgo 等也不例外）。

**11.2 实施纪律不是"建议"，是约束。**

任何因为没查文档导致的代码错误，应该直接重写，不要打补丁。
