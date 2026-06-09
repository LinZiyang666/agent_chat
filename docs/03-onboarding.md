# 03 · 新人上手指南

> 目标读者：第一次 clone repo 的工程师 / agent 作者。
> 目标：30 分钟跑通、找到代码、知道哪里别动。

---

## 1. 先决条件

| 工具 | 版本 |
|---|---|
| Go | 1.25+（`go.mod` 要求） |
| Linux / macOS / WSL | UDS 必备 |
| `make`、`git`、`bash` | 通常都有 |
| Discord 账号 + 自有 guild | 可选，做"真打 Discord"演练时用；本地跑 unit/smoke 不需要 |

> **WSL 注意**：`--data-root` 不要放 `/mnt/c/...`（DrvFs 不支持 UDS）。放 `~/.agentchat`。

---

## 2. 5 分钟跑通：本地无 Discord 也能用

```bash
git clone https://github.com/LinZiyang666/agent_chat.git
cd agent_chat
make build          # 产出 bin/agentchatd 和 bin/agentchat

# 起 daemon（前台 + 临时 data-root，看完 token 再按 ^C）
D=$(mktemp -d -t agentchat-XXXX)
./bin/agentchatd serve --data-root "$D"
# 关注 stdout 第一段输出，里面有：
#   AGENTCHAT_TOKEN=agch_xxxxxxxxxxxxxxxxxxxxxxxxxx
```

另开一个 shell：

```bash
export AGENTCHAT_HOME=$D
export AGENTCHAT_TOKEN=agch_xxxxxxxxxxxxxxxxxxxxxxxxxx

./bin/agentchat whoami
# ID:    01HZ...
# Name:  root
# Role:  admin
# State: created

./bin/agentchat state --json | jq .totals
# {"unread":0,"mentions":0,"priority":0,"announcements":0,"system_announcements":0}

./bin/agentchat watch state --json | jq --unbuffered '.version, .totals'
# 一行 0 / totals — 之后就静默，等事件。^C 退出。
```

> 没接 Discord 之前 `room create` 会返回 `INVALID_ARGUMENT: discord adapter has no guild_id configured`——这是预期。
>
> 想本地多账号联调走 mock：跑测试 / 写新测试，参考 `internal/api/*_test.go` 里 `mock.NewProber()` + `mock.Provider` 的用法。

---

## 3. 真要打 Discord 的最短路径

```bash
# 1) 在 https://discord.com/developers/applications 建 application
#    - Bot 页：Reset Token 拿一把 bot token（一次性可见！）
#    - Bot 页：开启两个 Privileged Intents：Server Members + Message Content
#    - OAuth2 → URL Generator：scopes=bot；permissions={View Channels, Send Messages,
#      Read Message History, Manage Channels, Manage Roles, Attach Files,
#      可选 Mention Everyone}
#    - 用生成的 URL 邀请 bot 进自己的 guild
#
# 2) Discord 客户端 User Settings → Advanced → Developer Mode = on
#    → 右键 server 图标 → Copy Server ID（18 位数字）
#
# 3) 配 guild_id：
export AGENTCHAT_DISCORD_GUILD_ID=123456789012345678   # 或写进 <data-root>/config.toml
# 重启 daemon 让配置生效
#
# 4) 接入 bot：
agentchat admin account create --name agent-alice --role user --json
ACC=01HZ...
agentchat admin account set-discord "$ACC" --bot-token "MTI..."   # daemon 会 Probe + 同步 name
agentchat admin account online "$ACC"
agentchat admin token create "$ACC" --json | jq -r .raw
# 把 raw 给 agent-alice 当 AGENTCHAT_TOKEN
#
# 5) 建 room 并拉人：
ROOM=$(agentchat room create --name proj-x --json | jq -r .id)
agentchat room invite "$ROOM" "$ACC" --subscribe
```

详细教程见 [`USAGE-ADMIN.md`](./USAGE-ADMIN.md)。

---

## 4. Repo 地图

```
agent_chat/
├── cmd/
│   ├── agentchatd/      ▷ daemon 入口 + serve / version / 数据锁
│   └── agentchat/       ▷ CLI 入口 + 所有 cobra verbs（一个文件一组）
│
├── internal/
│   ├── account/         account.Service (Build / PlanUpdate / Bootstrap)
│   ├── api/
│   │   ├── server.go    chi 路由 + 中间件链
│   │   ├── middleware/  Recover / Logger
│   │   └── v1/          所有 HTTP handler + DTO
│   ├── attachment/      后台 downloader（poll + backoff + sha256）
│   ├── audit/           audit.Action 枚举 + Recorder
│   ├── auth/            API token format / Manager / bearer middleware
│   ├── bot/
│   │   ├── provider.go  Provider interface（平台无关）
│   │   ├── events.go    EventConnected / Disconnected / MessageNew / ChannelDeleted
│   │   ├── mentions.go  ParseMentions（@<name> → <@uid>）
│   │   ├── types.go     Identity / Message / SendOptions / IdentityProber
│   │   ├── discord/     discordgo 实现 + Prober
│   │   └── mock/        内存测试双 + Prober
│   ├── cliutil/         PrintAndExit / IsTerminal
│   ├── config/          TOML + env，EnsureDataRoot(0o700)
│   ├── connector/       Provider 生命周期 + 事件 fan-out
│   ├── crypto/          bcrypt + AES-GCM + master.key 引导
│   ├── errcode/         Code 枚举 + ExitCode + HTTPStatus
│   ├── message/         inbound ingester（每账号一个 goroutine）
│   ├── state/           Aggregator + Bus（200 ms 防抖 + 版本号）
│   └── store/
│       ├── store.go     Bundler + Bundle + repo 接口
│       ├── types.go     所有领域类型
│       └── sqlite/      modernc 实现 + 嵌入式 migrations
│
├── pkg/
│   └── client/          公共 Go client（CLI / 第三方 SDK 都用）
│
├── e2e/                 mX-smoke.sh：每个 milestone 一份 bash 烟囱
├── scripts/             install.sh
├── skills/              SKILL.md（Claude Code agent 用）
├── reviews/             外审报告（M9-* 等）
├── docs/                这套文档；archive/ 是历史
├── Makefile
├── go.mod
└── build/goreleaser.yaml
```

### 4.1 看代码顺序建议

1. [`docs/00-overview.md`](./00-overview.md) → [`docs/01-architecture.md`](./01-architecture.md)（本系列）建立心智模型。
2. [`cmd/agentchatd/cmds/serve.go`](../cmd/agentchatd/cmds/serve.go)：daemon 怎么把所有组件接起来。
3. [`internal/store/store.go`](../internal/store/store.go) + [`internal/store/types.go`](../internal/store/types.go)：核心数据模型。
4. [`internal/api/server.go`](../internal/api/server.go)：所有 HTTP 路由 + 中间件接线。
5. 任意一个 handler，比如 [`internal/api/v1/messages.go::SendMessage`](../internal/api/v1/messages.go)：体会"tx 内 + tx 外 + bus.Publish"的标准结构。
6. [`internal/state/bus.go`](../internal/state/bus.go) + [`internal/state/aggregator.go`](../internal/state/aggregator.go)：state engine。
7. [`internal/bot/discord/discord.go`](../internal/bot/discord/discord.go) + [`internal/connector/connector.go`](../internal/connector/connector.go)：Discord 接入并发模型。

---

## 5. 测试 / 验证

| 命令 | 何时跑 |
|---|---|
| `go test ./internal/<pkg>/...` | 改某个包后；最快反馈 |
| `make test` | 提交前；跑全部 unit tests（无 race） |
| `make test-race` | milestone 关闭时；启 race detector，45 min budget |
| `make cover` | milestone 关闭时；输出 total coverage |
| `make smoke` | 真打 daemon + CLI，端到端验证两个 binary 还能用 |

> 项目惯例（来自 `MEMORY.md`）：开发循环优先单包 `go test`，`make test-race / smoke / cover` 留到 milestone 关闭时跑（最好后台跑）。

---

## 6. 常见任务 → 看哪里

| 任务 | 看 |
|---|---|
| 加一个 admin-only verb | `cmd/agentchat/cmds/admin_*.go` + `internal/api/v1/*.go`（handler）+ `internal/api/server.go`（路由） |
| 加一个新的 errcode | `internal/errcode/errcode.go` + `exitcode.go` + `http.go` 三处加 |
| 改 Snapshot 字段 | `internal/state/types.go` + `aggregator.go`；`USAGE-USER.md §6` / 02-implemented-requirements §7 同步更新 |
| 加新的 Bot Provider（如 Slack） | 复制 `internal/bot/discord/` 改成 `internal/bot/slack/`；保证实现 `Provider` + `IdentityProber` 接口；在 `serve.go::conn := connector.New(...)` 工厂里换 |
| 加新数据库表 | 新增 `internal/store/sqlite/migrations/000N_*.up.sql`；加 repo 接口到 `store/store.go` + 类型到 `store/types.go`；写 sqlite 实现；接到 `Bundle` + `WithTx` |
| 改 CLI 输出格式 | `cmd/agentchat/cmds/<verb>.go` 里的 `outputJSON()` 分支 |
| 加 audit action | `internal/audit/audit.go` 加 `Action` 常量；handler 里 `RecordVia(..., audit.NewAction, ...)` |

---

## 7. 调试小抄

- daemon 起不来：先 `agentchatd serve --log-level debug --data-root /tmp/xx 2>&1 | tail -50`；看 `flock` 报错 / `master.key` 长度 / migrations 失败。
- CLI 直接报 `Error [AUTH_MISSING]`：env / cli.toml 没生效——`agentchat whoami --json 2>&1` 看清楚是不是真没传。
- `watch state` 看上去卡：先 pipe 到 `cat -A` 排除 jq 缓冲；再确认有事件让它 publish（比如另一个终端 `send`）。
- bot 上线卡 `connecting`：99% 是 Privileged Intents 没勾 / token 带换行符。`agentchat admin account status <id> --json`，`provider_status` 会停在 `connecting`。
- 收不到消息：`agentchat debug events --account <id>` 看 gateway 是否真在喂；自己发的不会回显（Discord 不 echo 自己）。
- Audit 没记录：检查 handler 是不是在 `WithTx` 外调了 `recorder.Record`（错误用法）；应该是 `RecordVia(b.Audit, ...)`。

---

## 8. 风格 / 文化

- **不要**写多段 docstring；一两行 inline 注释解释 *为什么* 即可。识别 idiom：`// fix for M2-P3-005`、`// M9-P2: ...`。
- **不要**在事务里跑慢调用（bcrypt、Discord REST、文件 IO）；先在事务外算好，事务里只 INSERT/UPDATE/SELECT。
- **mutation 必 audit**，且与业务写共事务。
- **DTO / 字段名**写进 `internal/api/v1/types.go`；非 boundary 不要 leak 进 store 层。
- **CLI 子命令**一个文件一组，注册走 `init()`；尽量复用 `output.go` 里的 helper。
- **跨 milestone 改 DB schema**：新加 `migrations/000N_*.up.sql`，不动旧的；删列要走单独的 phase（参考 `0006_m9_drop_legacy_columns.up.sql`）。
- 项目惯例（`feedback_*` 记忆）：第三方库用前查最新官方文档；Phase 3 审核是用户主导，不要自宣布通过；Discord 互动模式下每条入站消息即时回复。

---

## 9. 文档地图

| 文件 | 内容 |
|---|---|
| [`00-overview.md`](./00-overview.md) | 一句话定位 / 哲学 / 进程拓扑 / 数据流 / 里程碑映射 |
| [`01-architecture.md`](./01-architecture.md) | 组件 / 数据模型 / 关键不变量 / 并发模型 / 路由清单 |
| [`02-implemented-requirements.md`](./02-implemented-requirements.md) | 当前实现满足的需求逐项核对 |
| [`03-onboarding.md`](./03-onboarding.md) | （本文）上手指南 |
| [`USAGE-ADMIN.md`](./USAGE-ADMIN.md) | 详尽运维手册（Discord 接入 / CLI 全清单 / 错误排查） |
| [`USAGE-USER.md`](./USAGE-USER.md) | 普通 user 手册（拿到 token 之后怎么干活） |
| [`archive/`](./archive/) | 旧 overview / requirements / architecture / roadmap / workflow / cli-redesign，考古时回看 |
| [`milestones/M*.md`](./milestones/) | 每个 milestone 的 phase 报告（实施 / 测试 / audit） |
| [`reviews/`](../reviews/) | 外审报告（M9 phase2、M9 CLI 重设计） |
| [`skills/agentchat-*.md`](../skills/) | Claude Code agent 操作 SKILL 文件 |

---

## 10. 不要踩的雷

1. 不要在事务里 bcrypt / Discord REST / 文件 IO（会卡住 SQLite 单写连接）。
2. 不要把 `bus.Publish` 放进 `WithTx` 闭包内（回滚也会触发重算）。
3. 不要直接读 SQLite / 看 daemon log 去演示 CLI 行为；走 CLI（`state` / `history` / `read` / `watch state` / ...）。
4. 不要在 mutation handler 里跳过 audit；尤其 diagnostic 类（`debug.send` 也得有）。
5. 不要回避 `errcode`，自己 `fmt.Errorf`——这会让 CLI 退码塌成通用 1。
6. 不要新增 verb 时绕过 cobra 的 `init() { ... AddCommand }` 注册模式。
7. 不要假设 `messages.discord_msg_id` 永远来自 send 路径或 ingest 路径任一边；两边可能任何顺序到，合并必须用 `CreateIgnoreConflict + ApplySendMetadata + MergeMentionEveryone + AddForMessage`。
8. 不要修改旧 migration 文件；只能新增。
9. 不要把 attachment 的下载路径暴露给 caller 之前——它可能还是 NULL（downloader 没下到呢）。caller 看到 `local_path == ""` 是正常的，应当退避。
10. 不要在不带 token 的命令上加业务逻辑；除 `/v1/healthz` 外所有 handler 都假定 `auth.AccountFromContext` 返回 actor。
