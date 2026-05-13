# Agent Chat — 实施路线图 v1.0

> 基线：`02-requirements-final.md` + `03-architecture.md`。
> 目标：把项目拆成 **8 个里程碑（M1–M8）**，每个都有可演示的完成标准，按依赖顺序推进。
> 原则：**早期不接 Discord**——前两个里程碑用 mock provider 跑通主干，先把"骨头"立起来；到 M3 才插 Discord 这块"肉"。这样能让 Bot 抽象层（D3.3）真正经受考验。

---

## 0. 一图看全

```
   M1  仓库骨架  ─────┐
                     ▼
   M2  账号 + 鉴权  ─────┐                  ← daemon 起来 + CLI 能调
                     ▼
   M3  Bot 抽象层 + Discord 接通 ─────┐    ← 第一条真实 Discord 消息
                     ▼
   M4  群 + 消息  ─────┐                  ← 核心 IM 能力闭环
                     ▼
   M5  状态界面（核心 UX）─────┐          ← agent 主回路打通
                     ▼
   M6  公告体系  ─────┐                  ← 三种公告 + 必读语义
                     ▼
   M7  附件下载  ─────┐                  ← 文件/图片本地索引
                     ▼
   M8  收尾  ─────────────────────────── ← 错误码 / 健康栏 / CLI 打磨
```

### 整体节奏（建议）

- **M1+M2**：最快推进，连接骨架，**不接 Discord**——验证 daemon/CLI/DB 主干。
- **M3**：项目最大的一道关——Bot 抽象层在这里第一次被真实平台压力测试。如果接 Discord 时发现 interface 设计不合理，**回头改 `bot.Provider`** 是 OK 的。
- **M4–M5**：连续做掉，因为 M5 状态界面要 M4 的消息流真的在跑。
- **M6/M7**：相对独立，**可以调换顺序**。
- **M8**：贯穿前面所有 M 的收尾——其实里面的事项可以提前做一部分。

---

## 1. 里程碑设计原则

1. **每个 M 都能 demo**：完成时能在终端上跑出可见结果，不只是"代码写完了"。
2. **依赖单调向前**：每个 M 只用前面 M 的产物，**不依赖后面**。
3. **接口早定义、实现可演进**：interface（`bot.Provider`、`store.Repo`）在 M2/M3 就稳定下来，后续只换实现。
4. **测试随做随补**：每个 M 至少有"主路径"的单元测试 + 一个 e2e demo 脚本。
5. **文档实时跟进**：每完成一个 M，更新 README 和 CLI 帮助。

---

## 2. M1 — 仓库骨架

### 目标
把目录、构建系统、入口都搭起来，**能 `go build` 出两个空的可执行文件**。

### 范围
- `go mod init github.com/LinZiyang666/agentchat`（Go 1.22+）
- 建出 `03-architecture.md §8` 全部目录（每个包先放 `doc.go` 占位）
- `cmd/agentchat/main.go`：cobra root，挂一个空 `version` 子命令
- `cmd/agentchatd/main.go`：cobra root，挂一个空 `version` 子命令
- `Makefile`：`build` / `test` / `fmt` / `vet` / `clean`
- `README.md`：项目一句话定位 + 构建 + 运行说明（边做边补）
- `.gitignore`：std Go + `*.db` + `*.db-wal` + `attachments/` + `agentchatd.sock` + `master.key` + `.idea/` `.vscode/`
- `LICENSE`：**[决策点 M1-1]** 选 MIT / Apache-2.0 / 暂不加？
- 加 deps：cobra（暂时只加 cobra，其它库到对应 M 再加）

### 完成标准（demo）
```bash
$ make build
$ ./bin/agentchat --help          # cobra 帮助输出
$ ./bin/agentchatd --help         # cobra 帮助输出
$ ./bin/agentchat version          # "agentchat dev"
$ go test ./...                    # PASS (no tests yet)
```

### 关键技术点
- 用 `cobra` 的 `cobra.Command` 而不是 `cobra-cli init` 生成器（生成器加的东西太多）。
- module path 用 `github.com/LinZiyang666/agentchat`。
- Makefile 里 `build` 输出到 `bin/`，路径明确。

### 工作量
小（1 个工作日内）。

---

## 3. M2 — 账号 + 鉴权（不接 Discord）

### 目标
daemon 能起来、CLI 能通过 token 操作账号系统的完整 CRUD。**Discord 完全不在这一里程碑里**——所有 Discord 字段先空着或 mock。

### 范围
#### daemon 侧
- 配置加载（TOML + env），默认根目录 `~/.agentchat/`
- 启动流程：
  1. 创建/解锁数据目录
  2. 打开 SQLite（modernc/sqlite），跑 migrations
  3. **如果 accounts 表为空**：自动建 root admin + 生成首启 token，**打印到 stdout**
  4. 监听 Unix Socket `~/.agentchat/agentchatd.sock`，chmod 600
- SQLite 迁移文件 v1（在 `internal/store/migrations/`）：
  - `accounts(id, name, role, bot_token_enc, lifecycle_state, created_at, ...)`
  - `tokens(id, account_id, token_hash, created_at, revoked_at, last_used_at)`
  - `audit_log(id, account_id, action, target, payload_json, created_at)`
- API endpoints（chi 路由 + handler）：
  - `POST /v1/accounts`（admin only）
  - `GET  /v1/accounts`
  - `GET  /v1/accounts/{id}`
  - `PATCH /v1/accounts/{id}`（改 name / role）
  - `DELETE /v1/accounts/{id}`
  - `POST /v1/accounts/{id}/tokens`（admin only，生成新 token）
  - `GET  /v1/accounts/{id}/tokens`
  - `DELETE /v1/tokens/{id}`（撤销）
  - `GET  /v1/whoami`
- Auth middleware：从 `Authorization: Bearer` 解 token → bcrypt 校验 → 注入 account 到 context
- audit middleware：所有 admin 操作落 `audit_log`

#### CLI 侧
- 通用：读取 token 三优先级（`--token` > `AGENTCHAT_TOKEN` > `~/.agentchat/cli.toml`）
- 子命令树（用 cobra）：
  ```
  agentchat whoami
  agentchat admin account create --name X --role user|admin
  agentchat admin account list
  agentchat admin account show <id>
  agentchat admin account delete <id>
  agentchat admin account set-role <id> <admin|user>
  agentchat admin token create <account-id>
  agentchat admin token list <account-id>
  agentchat admin token revoke <token-id>
  agentchat admin audit list [--account X] [--since ...]
  ```
- TTY 检测：tty 走人类表格输出，非 tty 走 JSON

### 完成标准（demo）
```bash
$ ./bin/agentchatd
[INFO] First-time setup: created admin 'root' with token: agch_a1b2c3...
[INFO] Listening on /home/me/.agentchat/agentchatd.sock

# 另一个终端
$ export AGENTCHAT_TOKEN=agch_a1b2c3...
$ ./bin/agentchat whoami
{"id":"...","name":"root","role":"admin"}

$ ./bin/agentchat admin account create --name agent1 --role user
{"id":"<id1>","name":"agent1","role":"user", ...}

$ ./bin/agentchat admin token create <id1>
{"token":"agch_xxxxx", "id":"...","account_id":"<id1>"}

$ AGENTCHAT_TOKEN=agch_xxxxx ./bin/agentchat whoami
{"id":"<id1>","name":"agent1","role":"user"}

$ ./bin/agentchat admin account list                 # 看到 root + agent1
$ AGENTCHAT_TOKEN=invalid ./bin/agentchat whoami     # 退出码非 0，stderr 错误码
```

### 关键技术点
- **修改 schema** = 加一个迁移文件，**不改老文件**。
- **crypto/master.key**：第一次启动时生成 32 字节随机写文件 chmod 600；存在则读。后续用于 AES-GCM 加密 bot_token（M3 用上）。
- bcrypt cost = 12（足够防爆破，CLI 调用频率不会高到性能瓶颈）。
- 错误响应统一 JSON：`{"error":{"code":"AUTH_INVALID","message":"...","details":{...}}}`，**错误码已经在 M2 里**长出来，不要拖到 M8。

### 工作量
中等（2–3 工作日）。这是基础架构层，多花一点时间换后面省事。

---

## 4. M3 — Bot 抽象层 + Discord 接通

### 目标
- 把 `internal/bot/` 的抽象层定义清楚（这是 **D3.3 的核心交付物**）
- Discord 实现能让一个 account "上线"，发出**第一条真实 Discord 消息**

### 范围
#### 抽象层（`internal/bot/`）
```go
// provider.go
type Provider interface {
    Connect(ctx context.Context) error
    Disconnect(ctx context.Context) error
    Status() ConnStatus

    Identity() Identity                            // 当前 bot 的身份信息
    SetIdentity(ctx, name, avatarURL) error

    SendMessage(ctx, channelID, content, opts) (*Message, error)
    EditChannel(ctx, channelID, ...) error
    DeleteChannel(ctx, channelID) error
    CreateChannel(ctx, name, opts) (channelID string, error)

    AddMember(ctx, channelID, memberID) error
    RemoveMember(ctx, channelID, memberID) error

    FetchHistory(ctx, channelID, before, limit) ([]*Message, error)

    Events() <-chan Event                          // 归一化事件流
}

// event.go
type Event interface { isEvent() }
type EventMessageNew struct{ ... }
type EventMessageReaction struct{ ... }
type EventMemberJoined struct{ ... }
// ...
```

#### Discord 实现（`internal/bot/discord/`）
- 用 `bwmarrin/discordgo`（**实施纪律 §11.1：先 WebFetch 查文档确认 API**）
- 把 discordgo 的事件 map 到我们的归一化 Event
- bot token 解密 → 启动 session → 订阅 Gateway 事件
- ⚠️ **必读文档点**：`discordgo` 的 Intents 配置——要明确开启 `MessageContent`、`GuildMessages`、`GuildMembers` 等

#### 集成
- `accounts.bot_token_enc` 字段启用（AES-GCM）
- 新增 API：
  - `POST /v1/accounts/{id}/discord` 设置 bot token + bot 信息
  - `POST /v1/accounts/{id}/online`（admin）→ daemon 触发 `Provider.Connect`
  - `POST /v1/accounts/{id}/offline`
- CLI:
  - `agentchat admin account set-discord <id> --bot-token <s>`
  - `agentchat admin account online <id>`
  - `agentchat admin account offline <id>`
  - `agentchat admin account status <id>`（含 Discord 连接态）

### 完成标准（demo）
前置：你在 Discord 上手动建好 1 个 application + bot，把 bot 加入测试 guild，记下 bot token 和测试 channel ID。

```bash
$ ./bin/agentchat admin account create --name agent1 --role user
$ ./bin/agentchat admin account set-discord <agent1-id> --bot-token <token>
$ ./bin/agentchat admin account online <agent1-id>
# Discord 客户端里能看到 agent1 上线

# 临时调试用的"裸发"命令（M3 暂用，M4 会替换为 room/message API）
$ ./bin/agentchat debug send --channel <channel-id> --text "hello from agentchat"
# Discord 客户端里能看到这条消息

# 在 Discord 客户端里手动发一条消息到该 channel
$ ./bin/agentchat debug events --account <agent1-id>     # NDJSON 实时打印
{"type":"message_new", "channel_id":"...", "content":"hi"}
```

### 关键技术点
- **必看 discordgo 文档**：Session 的生命周期、Intents、event handler 注册方式、Rate limit 处理。可能比想象中坑多。
- **重连策略**：discordgo 自带重连，但要确认与我们的 Disconnect 语义协调。
- **Bot token 加密**：AES-GCM，nonce 12 字节随机，密钥从 `master.key` 读取。
- **接口设计是 D3.3 的关键交付**：interface 要能用 mock 替换跑测试，未来加 Matrix 不用改业务层。

### 工作量
大（3–5 工作日）。这是项目的关键节点——花时间是值得的。

---

## 5. M4 — 群 + 消息

### 目标
完整的群 CRUD（包括三态成员关系）+ 消息收发与本地缓存。**Discord channel 1:1 映射 Room**。

### 范围
#### SQLite 迁移 v2
- `rooms(id, discord_channel_id, name, archived, created_at, ...)`
- `memberships(account_id, room_id, subscribed, joined_at, PRIMARY KEY(account_id, room_id))`
- `messages(id, room_id, author_account_id, discord_msg_id, content, reply_to_msg_id, requires_ack, priority, created_at, content_hash)`
- `message_states(message_id, account_id, read_at, replied_at, PRIMARY KEY(message_id, account_id))`
- `reactions(message_id, account_id, emoji, created_at)`

#### API
- 群 CRUD：
  - `POST /v1/rooms`（admin only）→ daemon 调 `bot.CreateChannel` 建 Discord channel
  - `GET /v1/rooms`（admin 看全部，user 看自己 membership 内的）
  - `PATCH /v1/rooms/{id}`（改名）
  - `POST /v1/rooms/{id}/archive`
  - `DELETE /v1/rooms/{id}`
- 成员管理（admin only）：
  - `POST /v1/rooms/{id}/members` body `{account_id, subscribed: bool}` → DB + Discord 权限同步
  - `DELETE /v1/rooms/{id}/members/{account_id}` → 同步 Discord 权限移除
  - `PATCH /v1/memberships/{room_id}` body `{subscribed: bool}`（user 可改自己的订阅）
- 消息：
  - `POST /v1/rooms/{id}/messages` body `{content, reply_to?, requires_ack?, priority?}` → `bot.SendMessage` + 落 DB
  - `GET /v1/rooms/{id}/messages?before=&limit=`
  - `POST /v1/messages/{id}/read`
  - `POST /v1/messages/{id}/reply-ack`

#### Bot 适配
- 监听 `EventMessageNew` → 落 DB（per-account state 全部初始为 unread）
- 监听 `EventMemberJoined/Left` → 更新 memberships

#### CLI
```
agentchat room create --name X
agentchat room list
agentchat room show <id>
agentchat room archive <id>
agentchat room delete <id>
agentchat room invite <room-id> <account-id> [--subscribe]
agentchat room kick <room-id> <account-id>
agentchat room subscribe <room-id>      # user 自己改
agentchat room unsubscribe <room-id>

agentchat send <room-id> "text"
agentchat send <room-id> --file -        # stdin
agentchat send <room-id> --reply <msg-id> "text"
agentchat history <room-id> [--before=...] [--limit=...]
agentchat read <msg-id>
agentchat reply-ack <msg-id>
```

### 完成标准（demo）
```bash
# admin 视角
$ ./bin/agentchat room create --name ops
$ ./bin/agentchat room invite <ops-id> <agent1-id> --subscribe
$ ./bin/agentchat room invite <ops-id> <agent2-id>          # 入群不订阅

# user agent1 视角
$ export AGENTCHAT_TOKEN=<agent1-token>
$ ./bin/agentchat send <ops-id> "Hello from agent1"
$ ./bin/agentchat history <ops-id>

# 真实 Discord 客户端里也能看到这条消息
# 在 Discord 客户端里回一条 → user agent1 拉取 history 时能看到
```

### 关键技术点
- **群 = Discord channel 1:1**：建群时同步建 channel，删群时删 channel。
- **成员同步**：加成员 → Discord channel 给该 bot 加权限；移除反之。
- **消息双源**：发消息是 "先 send 到 Discord → 拿到 discord_msg_id → 落 DB"；收消息是 "Discord event → 落 DB"。两路要在同一 schema 下统一。
- **content_hash**：发消息时计算 SHA-256，用于 C20 一致性校验。

### 工作量
大（4–6 工作日）。

---

## 6. M5 — 状态界面（核心 UX）

### 目标
按 D5 决策实现状态聚合引擎 + `watch state` NDJSON 流。

### 范围
- `internal/state/`:
  - `aggregator.go`：从 DB 拉数据，组装 8 维度 snapshot
  - `subscriber.go`：管理 watch 订阅者
  - `bus.go`：内部事件总线，业务层（message/room/membership/...）有变化时往 bus 发，bus 触发 200ms debounce 后通知 subscribers
- API:
  - `GET /v1/state` 一次性快照
  - `GET /v1/state/watch[?since=<version>]` NDJSON 流
- CLI:
  - `agentchat state`（一次性，TTY 走人类彩色 / 非 TTY 走 JSON）
  - `agentchat watch state`（流式）

### 完成标准（demo）
```bash
# 终端 1: agent1 watch
$ export AGENTCHAT_TOKEN=<agent1-token>
$ ./bin/agentchat watch state
{"version":1, "totals":{...}, "rooms":[...], "mentions":[], ...}
# (静默等待)

# 终端 2: agent2 发消息 @agent1
$ AGENTCHAT_TOKEN=<agent2-token> ./bin/agentchat send <room-id> "hey @agent1"

# 终端 1 在 200ms 内输出新一行：
{"version":2, ..., "mentions":[{"msg_id":"...", "room":"...", ...}]}

# 验证静默：终端 1 在没事件的 10 秒里不发任何字节（用 hexdump 验证）
```

### 关键技术点
- **聚合查询性能**：8 维度 snapshot 不能每次都 N 个 query，要写组合 SQL。
- **debounce 实现**：用 `time.AfterFunc` + 取消重置。
- **多 subscriber 广播**：fan-out channel pattern。
- **version 单调**：用 `atomic.Int64`，每次变更 +1。
- **空闲不写字节**：测试时用 `socat - UNIX-CONNECT:...sock` 抓真实流量验证。

### 工作量
中等偏大（3–5 工作日）。

---

## 7. M6 — 公告体系

### 目标
三种公告（群公告 / @all / 系统公告）+ 必读语义全部跑通。

### 范围
#### SQLite 迁移 v3
- `announcements(id, room_id, content, version, created_by, created_at)`
- `announcement_reads(announcement_id, account_id, read_at, PRIMARY KEY(announcement_id, account_id))`
- `system_announcements(id, content, created_by, created_at)`
- `system_announcement_reads(sys_ann_id, account_id, read_at, PRIMARY KEY(sys_ann_id, account_id))`
- `messages` 加一列 `mention_all BOOLEAN`

#### API
- `POST /v1/rooms/{id}/announcement` body `{content}` → 新版本 announcement，**所有现有 member 标记为未读**（强制必读）
- `GET /v1/rooms/{id}/announcement`
- `POST /v1/announcements/{id}/read`
- `POST /v1/system/announcements`（admin only）
- `GET /v1/system/announcements`
- `POST /v1/system/announcements/{id}/read`
- 修改 `POST /v1/rooms/{id}/messages`：增加 `mention_all: true` 选项

#### CLI
```
agentchat room announce <room-id> "text"
agentchat room announce-show <room-id>
agentchat ack-announcement <announcement-id>

agentchat admin system-announce "text"
agentchat system-announcements
agentchat ack-system <id>

agentchat send <room-id> --all "broadcast text"
```

#### 状态界面集成（修改 M5 的 aggregator）
- 维度 6（新加入的群带必读公告）开始有真实数据
- 维度 5（紧急 / 系统级单独区）展示系统公告

### 完成标准（demo）
```bash
$ ./bin/agentchat room announce <room> "重要：本群启用 v2 协议"
# 所有现有成员状态界面里：维度 6 多一项

$ ./bin/agentchat admin account create --name agent3 --role user ...
$ ./bin/agentchat room invite <room> <agent3-id> --subscribe
# agent3 的状态界面里：维度 6 显示该群有必读公告 + announcement 是 unread

$ ./bin/agentchat send <room> --all "@all 演练开始"
# 所有成员的维度 3（mentions）都有这条

$ ./bin/agentchat admin system-announce "维护通知：本周日 2:00 重启"
# 所有 user 的维度 5 出现
```

### 工作量
中等（2–3 工作日）。

---

## 8. M7 — 附件下载

### 目标
消息带附件时，daemon 主动下载到本地、维护索引；CLI 展示索引让用户/agent 自己用本地工具打开。

### 范围
#### SQLite 迁移 v4
- `attachments(id, message_id, filename, size, mime, local_path, discord_url, downloaded_at, sha256)`

#### 下载器
- `internal/attachment/downloader.go`：订阅 message_new 事件 → 看附件 → 异步下载 → 落 `local_path`
- 路径：`~/.agentchat/attachments/<room-id>/<message-id>/<filename>`
- 失败重试 + 退避
- 大文件先发 HEAD 拿 Content-Length 校验

#### API
- `GET /v1/rooms/{id}/messages` 响应里附件部分含 `local_path` 和 `discord_url`
- `POST /v1/messages` body 支持 `attachments: [{path: "/local/file"}]` → daemon 读本地文件上传到 Discord（**校验大小 ≤ 25MB**，超限返回错误码 `ATTACHMENT_TOO_LARGE`）

#### CLI
```
agentchat send <room-id> --attach /path/to/file.png "text"
agentchat history <room-id>          # 列出消息时把附件索引一并显示
```

### 完成标准（demo）
```bash
$ ./bin/agentchat send <room> --attach screenshot.png "look at this"
# Discord 客户端里能看到带图片的消息

# 另一个 agent 收到消息后
$ ./bin/agentchat history <room>
... [ATTACHMENT] screenshot.png (243KB, image/png) → ~/.agentchat/attachments/<room>/<msg>/screenshot.png
$ xdg-open ~/.agentchat/attachments/<room>/<msg>/screenshot.png

# 超大文件
$ ./bin/agentchat send <room> --attach huge.zip   # 50MB
ERROR: ATTACHMENT_TOO_LARGE - file exceeds Discord 25MB limit (50.3MB)
$ echo $?
22                                                 # 专门的退出码
```

### 工作量
中等（2–3 工作日）。

---

## 9. M8 — 收尾

### 目标
把架构里所有"我们说会做"但前面还没做的细节补齐，让项目达到"agent 可以无脑用 + 人可以舒服用"。

### 范围
- **错误码完整枚举（`internal/errcode/`）**：把前面 M 里 ad-hoc 的字符串错误码整理成统一 enum + JSON 编码 + 退出码映射表
- **系统健康栏（`internal/health/`）**：
  - 探活 Discord API（定期 noop call）
  - bot token 校验状态
  - 网络状态
  - daemon 状态界面里的"维度 8"开始有真实数据
- **CLI 体验打磨**：
  - 表格输出（用 `tablewriter` 之类）
  - 彩色（`fatih/color`，仅 tty 时启用）
  - 进度条（下载附件时）
  - shell 补全：cobra 内置 `completion bash/zsh/fish`
- **速率限制与重试**：
  - Discord API 自带 rate limit，discordgo 内部处理但要确认行为正确
  - 我们自己的 API 不加 rate limit（本地工具）
- **文档**：
  - `docs/CLI-MANUAL.md`：完整 CLI 命令参考
  - `docs/API-SPEC.md`：HTTP API 文档（OpenAPI 也行）
  - `docs/OPS-GUIDE.md`：怎么启动 daemon、怎么备份、怎么排错
  - README 更新到完整状态
- **systemd unit 文件**（可选）：`agentchatd.service` 模板放 `scripts/`
- **集成测试**：
  - 一个 `e2e/` 目录，包含端到端 demo 脚本（mock provider 版本，CI 友好）
  - 单测覆盖率 ≥ 60% 即可，不强求

### 完成标准
- 所有错误情况都有结构化错误码 + 退出码
- 健康栏在状态界面里能反映 token 失效 / Discord API down / 网络断
- 完整的 README + 三份 docs
- `make test` 全过 + 覆盖率报告
- 一个新人能照 README 在 30 分钟内跑起来

### 工作量
中等（3–5 工作日，但可以贯穿前面 M 持续做一部分）。

---

## 10. 跨里程碑的工程实践

### 10.1 提交粒度
- 每个 M 内部拆成 5–10 个 commit
- commit message 风格：`<scope>: <imperative summary>`（如 `auth: add bcrypt token verifier`）
- **不加 `Co-Authored-By: Claude`**（用户全局规则）

### 10.2 测试策略
| 层 | 测试方式 |
|---|---|
| `internal/store/` | 用 `:memory:` SQLite 跑真实迁移 + CRUD |
| `internal/account/`、`room/`、`message/` 等业务包 | 注入 mock `bot.Provider` |
| `internal/bot/discord/` | 不写单测（依赖外部服务），用手动 e2e |
| `internal/state/` | 表驱动测试 + debounce 时序测试 |
| `internal/api/` | httptest 起 server，用 `pkg/client` 调 |

### 10.3 不在第一版做的事（避免 scope creep）
- 任何 Web UI
- 任何 IDE 集成
- 任何 LLM 调用（项目定位决定）
- 多 guild 支持
- 消息搜索的全文索引（SQLite FTS5 留 hook 但不实装）
- shell 补全的安装脚本（手动 `complete -F` 即可）
- 国际化 / i18n
- 包发布（cargo / homebrew tap 等）

### 10.4 风险与未决事项

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| discordgo 的 Intents 配置 / event 漏接 | 中 | 中 | M3 时仔细查文档，写一个手动 e2e 脚本反复验证 |
| Discord API rate limit 撞墙 | 中 | 中 | discordgo 内置 backoff，但批量操作要自己做节流 |
| modernc/sqlite 性能问题 | 低 | 中 | 量级小不会有，真撞了切 mattn/go-sqlite3 |
| ≤ 10 application 不够用 | 中 | 高 | 已在 D3.1 接受；超过时回头讨论 |
| Bot 抽象层接口设计错误 | 中 | 高 | M3 故意当作压力测试；接口可改 |
| Discord 政策变动（封 bot / 改 API） | 低 | 高 | 抽象层 + 换 Matrix 兜底 |

---

## 11. 决策点回顾（M1-1 LICENSE）

唯一在路线图里冒出来的新决策：

### M1-1 LICENSE
- **(1) MIT**（推荐——最宽松、最常见、最少争议）
- (2) Apache-2.0（更明确专利授权，但文件长）
- (3) 暂不加（不开源时可以，但仓库挂 GitHub 默认建议加一个）
- (4) 别的：______

---

## 12. 怎么开始

最自然的工作流：

```
Step 1  → 你确认本路线图（或指出要改的 M）
Step 2  → 你决定 M1-1（LICENSE）
Step 3  → 我开 M1 任务，进入 Phase 1（编码）→ Phase 2（测试）→ Phase 3（外部审计）
Step 4  → 三阶段全部 done 后我汇报，等你 review/接受/调整再继续
```

不要一口气把 8 个 M 全做完——**每个 M 完成后停下来让你 review** 是路线图的核心控制点。

每个 M 内部强制走 **三阶段工作流**（编码 → 测试 → 外部审计），详见
[`05-engineering-workflow.md`](./05-engineering-workflow.md)。**没有 Phase 3 通过，M 不算 done。**
