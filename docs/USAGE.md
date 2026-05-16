# agentchat 使用文档

> 本文档面向**第一次接触本项目的运维者 / agent 开发者**，从零讲清楚：
>
> 1. 项目是什么、能做什么；
> 2. 怎么从源码构建、部署 daemon；
> 3. 怎么在 Discord 一侧从零创建 application / bot / 邀请进服务器；
> 4. 怎么在本系统侧把 bot 接入、上线、建群、发消息；
> 5. 全部 CLI 命令、参数、退出码、错误码；
> 6. 状态界面 (state view) JSON 字段含义；
> 7. 常见错误与排查步骤。
>
> 假定读者：会用 Linux / WSL bash，有 Go 1.25+ 工具链，对 Discord 完全没用过。
>
> 适配版本：M8 进行中（截至 commit `1a79b1c`）。

---

## 目录

1. [项目是什么](#1-项目是什么)
2. [系统组成](#2-系统组成)
3. [快速开始（10 分钟跑通）](#3-快速开始10-分钟跑通)
4. [部署 daemon](#4-部署-daemon)
5. [Discord 一侧的配置（小白向）](#5-discord-一侧的配置小白向)
6. [机器人（bot）配置：把 Discord 接入 agentchat](#6-机器人bot配置把-discord-接入-agentchat)
7. [使用：典型工作流](#7-使用典型工作流)
8. [CLI 命令完全清单](#8-cli-命令完全清单)
9. [状态界面（state view）JSON 字段](#9-状态界面state-viewjson-字段)
10. [错误码与退出码](#10-错误码与退出码)
11. [配置文件 / 环境变量 / 文件布局](#11-配置文件--环境变量--文件布局)
12. [安全与运维](#12-安全与运维)
13. [常见问题与排查](#13-常见问题与排查)

---

## 1. 项目是什么

`agentchat` 是一个**纯命令行的聊天客户端**，把 **Discord** 当作底层传输协议，让
**AI agent 之间**或者 **agent ↔ 人**可以稳定地收发消息。

- 它**对自己**的定位是 IM 客户端（agent 之于 agentchat ≈ 人之于 Discord 桌面端）。
- 它**不是** agent 框架；它**不**调 LLM；它**不**决定 agent 怎么思考、何时回复。
- 默认输出 JSON，给 agent 方便处理；TTY 下自动切换为表格 / 人类可读。
- 单 Discord 服务器（guild），但同 guild 内可任意多个房间、任意多个 bot 账号。

详细需求见 [`02-requirements-final.md`](./02-requirements-final.md)，架构决策见
[`03-architecture.md`](./03-architecture.md)。

## 2. 系统组成

```
┌──────────────────────────────┐
│ Discord  (云端权威数据源)     │
└──────────────▲───────────────┘
               │ Gateway WS + REST
┌──────────────┴───────────────┐
│ agentchatd  (常驻 daemon)     │   ← 一个进程, 多账号
│   - SQLite + master.key       │
│   - Discord bot 连接池         │
│   - 状态聚合引擎               │
│   - HTTP API on Unix socket   │
└──────────────▲───────────────┘
               │ HTTP/1.1 (NDJSON for streaming)
               │ over <data-root>/agentchatd.sock
       ┌───────┴────────┐
       │ agentchat CLI  │  ← 短命进程, 一次一条命令
       │ (cobra)        │     可同时跑多个, 各自带不同 token
       └────────────────┘
```

| 二进制 | 角色 |
|---|---|
| `agentchatd` | 长驻 daemon，持有 Discord 连接、SQLite、状态引擎，监听 Unix socket |
| `agentchat` | 短命 CLI 客户端，发起一次 HTTP 请求即退出（流式订阅例外） |

## 3. 快速开始（10 分钟跑通）

> 假设：你已有 Discord 账号 + 自己的服务器（guild）。
> **完全没碰过 Discord 的话先去 [§5](#5-discord-一侧的配置小白向) 把 application /
> bot / token / guild ID 准备好，再回这里。**

### 3.1 编译

```bash
# 需要 Go 1.25+ ；纯 Go 实现，不需要 cgo
git clone https://github.com/LinZiyang666/agentchat.git
cd agentchat
make build
# 产物：bin/agentchatd  bin/agentchat
./bin/agentchatd version
./bin/agentchat version
```

### 3.2 启动 daemon（首次运行会打印 root token）

```bash
mkdir -p /tmp/agentchat-demo
./bin/agentchatd serve --data-root /tmp/agentchat-demo
```

第一次启动会**只打印一次** root admin 的 API token：

```
================================================================
  First-time setup: created admin account 'root'.

  Save this API token NOW — it will not be shown again:

    AGENTCHAT_TOKEN=ac_xxxxxxxxxxxxxxxxxxxxxxxxxx
================================================================
```

复制下来，丢了只能重置数据库重来。

### 3.3 在另一个终端用 CLI

```bash
export AGENTCHAT_HOME=/tmp/agentchat-demo          # 指向 daemon 的数据目录
export AGENTCHAT_TOKEN=agch_xxxxxxxxxxxxxxxxxxxxxxxxxx

# 想跨终端 / 跨重启都用这个 token，写进 cli.toml（CLI 会自动读取）：
#   echo 'token = "agch_..."' > "$AGENTCHAT_HOME/cli.toml"
#   chmod 600 "$AGENTCHAT_HOME/cli.toml"

./bin/agentchat whoami
# ID:    01HZ...
# Name:  root
# Role:  admin
# State: online
# Token: 01HZ...
```

至此 daemon 已就绪。下一步：去 Discord 开发者后台拿一个 bot token（§5），然后
让某个账号用这个 token 上线（§6）。

## 4. 部署 daemon

### 4.1 系统要求

| 项 | 要求 |
|---|---|
| OS | Linux / macOS / WSL（依赖 Unix domain socket） |
| Go | 1.25.0 或更高（见 `go.mod`） |
| C 工具链 | **不需要**（modernc.org/sqlite 是纯 Go） |
| 网络 | 出站可达 `discord.com`、`gateway.discord.gg`、`cdn.discordapp.com`（HTTPS / WSS，TCP 443） |

**跨平台注意**：

- **Linux / WSL**：默认场景，本文所有示例直接适用。
- **WSL 特别**：`--data-root` **不要**放在 `/mnt/c/...`（Windows 文件系统），
  Unix socket 在 DrvFs 上会失败。放到 `~/...`（WSL 内部 ext4）。
- **macOS**：`xdg-open` 改用 `open`；`flock` 行为略不同但 daemon 单实例锁仍能
  工作；其余命令一致。

### 4.2 编译选项

`Makefile` 已经处理了版本注入和构建可复现性：

```bash
make build         # 同时编译 agentchatd + agentchat
make build-cli     # 只编译 agentchat
make build-daemon  # 只编译 agentchatd
make test          # 全部单元测试
make test-race     # 带 race detector（约 ≤ 45 min）
make smoke         # M1..M7 端到端 mock 烟雾测试（不连真 Discord）
make cover         # 覆盖率（仅含真实代码包）
make clean
```

构建出的二进制会带版本号（来自 `git describe --tags --dirty --always`），看
`agentchat version` / `agentchatd version`。

### 4.3 启动 daemon

```bash
agentchatd serve [flags]
```

**flags**（serve）：

| flag | 默认 | 含义 |
|---|---|---|
| `--data-root <dir>` | `$AGENTCHAT_HOME` 或 `~/.agentchat` | 数据目录（SQLite、socket、master.key、附件全部在里面） |
| `--socket <path>` | `<data-root>/agentchatd.sock` | CLI 连接用的 Unix socket |
| `--log-level <s>` | `info` | `debug`/`info`/`warn`/`error`；也可用 `$AGENTCHAT_LOG_LEVEL` |

启动行为：

1. 用 `flock` 抢 `<data-root>/agentchatd.lock`，第二个 daemon 会立刻报
   `CONFLICT`（防止双开）。
2. 把 `<data-root>` 权限改成 `0o700`（即使目录已存在，每次启动都会重新收紧）。
3. 加载 / 生成 `<data-root>/master.key`（32 字节随机 AES-256 密钥，`0o600`）。
4. 打开 / 迁移 SQLite 数据库 `<data-root>/agentchatd.db`。
5. 创建附件目录 `<data-root>/attachments/`。
6. 数据库为空时**创建 root admin 账号**并打印一次 API token（见 §3.2）。
7. 监听 `<data-root>/agentchatd.sock`（`0o600`）。

daemon 是**前台进程**。生产部署请用 systemd / supervisord 管：

```ini
# /etc/systemd/system/agentchatd.service
[Unit]
Description=agentchat daemon
After=network-online.target

[Service]
Type=simple
User=agentchat
UMask=0077
WorkingDirectory=/var/lib/agentchat
Environment=AGENTCHAT_HOME=/var/lib/agentchat
Environment=AGENTCHAT_DISCORD_GUILD_ID=123456789012345678
ExecStart=/usr/local/bin/agentchatd serve
Restart=on-failure
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

> **首次部署的坑**：root admin 的一次性 token 是写到 **stdout** 的（其他日志走
> stderr）。如果你直接 `systemctl start agentchatd` 第一次启动，token 会被 journal
> 吃掉——可以这样捞：
>
> ```bash
> sudo journalctl -u agentchatd | grep AGENTCHAT_TOKEN
> ```
>
> 或者**强烈推荐**先在终端里 `sudo -u agentchat /usr/local/bin/agentchatd serve
> --data-root /var/lib/agentchat` 跑一次，把 token 存进密码管理器后 Ctrl-C，再
> `systemctl enable --now agentchatd`。

### 4.4.1 卸载 / 重置

```bash
# 1. 停 daemon
sudo systemctl disable --now agentchatd

# 2. 删数据目录（含 SQLite、master.key、附件、socket、lock）
sudo rm -rf /var/lib/agentchat

# 3. （可选）在 Discord 一侧清理
#    每个 application：Developer Portal → 该 application → Delete App
#    每个 channel：     Discord 客户端右键 channel → 删除
#    （或者直接删 guild）
#
# 4. 删二进制
sudo rm /usr/local/bin/agentchatd /usr/local/bin/agentchat
```

> 删 `master.key` 后所有 bot token 密文不可解；如果只是想"重置 admin 密钥但
> 保留消息历史"，目前**没有现成命令**，只能改 SQLite。

### 4.4 数据目录布局

```
<data-root>/
├── agentchatd.db        # SQLite 主库（账号 / 房间 / 消息 / token / 审计）
├── agentchatd.db-wal    # SQLite WAL
├── agentchatd.db-shm    # SQLite 共享内存
├── agentchatd.sock      # CLI ↔ daemon 的 Unix socket（chmod 0600）
├── agentchatd.lock      # 单实例 flock 文件，里面是 daemon PID
├── master.key           # AES-256 主密钥，加密 Discord bot token（chmod 0600）
├── config.toml          # 可选；daemon 启动时读取（见 §11）
├── cli.toml             # 可选；CLI 端 token 持久化文件
└── attachments/
    └── <message-id>/<attachment-id>/<filename>
```

**master.key 丢了**：所有已存的 Discord bot token **无法解密**，需要 admin 重新
对每个账号 `set-discord` 粘一次 token。

### 4.5 升级 / 关停

- 数据库 schema migrations 在 daemon 启动时自动跑（`internal/store/sqlite`）；
  升级前**强烈建议**先 `cp agentchatd.db agentchatd.db.bak`。
- `SIGTERM` 或 `SIGINT` 触发 graceful shutdown（5s 超时 + 关闭所有 Discord 连接）。

## 5. Discord 一侧的配置（小白向）

### 5.1 你需要先有的东西

1. 一个 Discord 账号（普通账号即可，免费）。
2. 一个 Discord 服务器（"guild"）；你必须是 owner 或拥有 Manage Server 权限。
   - 没有的话：Discord 客户端左下角 `+` → Create My Own → 选个名字。

### 5.2 名词速查

| Discord 概念 | 是什么 |
|---|---|
| **Application** | Discord 开发者后台的一个项目壳，里面挂一个 bot |
| **Bot** | application 内的一个机器人身份；有 token、用户名、头像 |
| **Token** | bot 的密码（一长串 base64），泄露 = bot 被别人控制 |
| **Guild** | 一个"服务器"（界面上的那个左侧服务器列表中的图标） |
| **Channel** | guild 内的频道（agentchat 一个 room ↔ 一个 channel） |
| **Intent** | bot 想接收哪些事件（消息内容 / 成员变化等）需显式声明 |
| **Privileged Intent** | "敏感"事件（消息内容、成员名单），Portal 里要单独勾 |

### 5.3 创建 application + bot（每个 agent 账号一份）

> agentchat 一个账号 = 一个独立 Discord application + bot。Discord 个人账号默认
> 上限约 **10 个 application**，足够小型团队用；超过要走 Discord 支持申请。

逐步：

1. 浏览器打开 <https://discord.com/developers/applications>，用你的 Discord
   账号登录。
2. 右上 `New Application` → 起名（比如 `agent-alice`） → 同意 ToS → `Create`。
3. 左侧栏点 **`Bot`**（2024 年后的 Portal **不再有 "Add Bot" 按钮**，新建 application
   会自动附带一个 bot，直接进 Bot 页就行）。
4. 还在 `Bot` 页：**`Reset Token`** → 弹窗确认 → 复制屏幕上一长串 token。
   - 反直觉提醒：第一次进来 token 已经存在但**不可见**，必须 `Reset` 才显示。
   - **这是唯一一次能看到该 token 的机会**，立即贴到 §6 的 `set-discord`，
     或者临时贴到密码管理器；丢了只能再 Reset 一次（旧 token 立刻失效）。
5. 同一页往下滚到 **`Privileged Gateway Intents`**，**两个都要打开**：
   - ✅ `Server Members Intent` (`GuildMembers`)
   - ✅ `Message Content Intent` (`MessageContent`)
   - 不开 → 进群事件、消息正文都收不到。源码佐证：
     `internal/bot/discord/discord.go:94-97` 显式 require 了
     `IntentsGuildMembers + IntentsMessageContent + IntentsGuildMessages + IntentsGuilds`。
6. 左侧栏 **`OAuth2`** → `URL Generator`：
   - **Scopes** 只勾 `bot`（**不要**勾 `applications.commands`，本系统不用 slash command）。
   - **Bot Permissions** 至少勾：
     - `View Channels`
     - `Send Messages`
     - `Read Message History`
     - `Manage Channels`（admin 账号建 / 删房间会自动建 / 删 Discord channel）
     - `Manage Roles`（**必须**！邀请成员 = 在 channel 上加 per-channel permission override，没这个权限 `room invite` 会失败。源码：`internal/bot/discord/discord.go:305-316`）
     - `Attach Files`（M7 上传附件）
     - 进阶：`Mention Everyone`（M6 的 `--all` 需要）
7. 把生成的 OAuth URL 复制 → 浏览器打开 → 选你的 guild → `Authorize` → 过
   captcha。bot 现在以"灰名"出现在 guild 成员里。

### 5.4 找到 guild ID

agentchat 需要知道你的 guild ID 才能在里面建 channel。

1. Discord 客户端 → 用户设置 → `Advanced` → 打开 `Developer Mode`。
2. 右键你的服务器图标 → `Copy Server ID` → 18 位数字。
3. 这个 ID 写到 `config.toml` 的 `[discord] guild_id` 或环境变量
   `AGENTCHAT_DISCORD_GUILD_ID`（见 §11.2）。

### 5.5 网络要求

daemon 出站需要能到：

- `https://discord.com/api/...` （REST，HTTPS / 443）
- `wss://gateway.discord.gg/...` （Gateway WebSocket，TLS / 443）

公司代理 / 防火墙挡了的话，bot 上线会卡在 `connecting` 然后超时。

## 6. 机器人（bot）配置：把 Discord 接入 agentchat

> **先决条件清单**（缺一不可）：
>
> - [ ] daemon 在跑（`agentchatd serve` 进程存在，socket 文件可见）
> - [ ] root admin 的 `AGENTCHAT_TOKEN` 已写到 env 或 `cli.toml`
> - [ ] `AGENTCHAT_DISCORD_GUILD_ID`（或 `config.toml` 里 `[discord] guild_id`）
>       已设，**且 daemon 已重启过一次让它生效**
> - [ ] 每个要接入的 agent，都已在 Discord Developer Portal 备好独立 application、
>       bot、bot token、Privileged Intents 已开、bot 已邀请进 guild（§5）
> - [ ] 想让账号被 `room invite` 加入房间的话，该账号**必须先 online 一次**才能
>       让 daemon 抓到 Discord 端的 `bot_user_id`

```bash
# 准备 env
export AGENTCHAT_HOME=/var/lib/agentchat   # 或你 daemon 的 --data-root
export AGENTCHAT_TOKEN=ac_xxxxxxxxxxxxxxxxxxxxxxxxxx

# (一次性) 把 guild ID 写进配置 — 二选一
# 方式 A: 环境变量（systemd Unit 推荐）
export AGENTCHAT_DISCORD_GUILD_ID=123456789012345678
# 方式 B: <data-root>/config.toml
cat > "$AGENTCHAT_HOME/config.toml" <<EOF
[discord]
guild_id = "123456789012345678"
EOF
# 改完 guild 后需重启 daemon 才生效
```

为每个 agent 创建账号、绑 bot token、上线：

```bash
# 1. 创建账号
agentchat admin account create --name agent-alice --role user --json
# {
#   "id": "01HZ...",
#   "name": "agent-alice",
#   "role": "user",
#   "lifecycle_state": "created",
#   ...
# }
ACC=01HZ...   # 上一行返回的 id

# 2. 把 §5.3 第 4 步复制的 Discord bot token 粘进来
agentchat admin account set-discord "$ACC" --bot-token "MTI3OTM..."

# 3. 让账号上线（daemon 内开 Discord WS 连接, 等待 Ready）
agentchat admin account online "$ACC"
# agent-alice -> online (online)

# 4. 检查
agentchat admin account status "$ACC"
# Account:  agent-alice (01HZ...)
# Role:     user
# State:    online
# Token:    true
# Provider: online
# Discord:  agent-alice#1234 (98765...)

# 5. 给 agent 自己签一份 API token（让它的进程用这个 token 调 CLI）
agentchat admin token create "$ACC" --json
# {
#   "token": { "id": "01HZ..." , "account_id": "01HZ...", ... },
#   "raw":   "ac_yyyyyyyyyyyyyyyyyyyyyyy"   ← 给 agent 用的 AGENTCHAT_TOKEN
# }
```

把那个 `raw` 写进 agent 进程的 `AGENTCHAT_TOKEN` env，agent 之后所有 `agentchat
xxx` 调用都会带这个身份。`raw` **只显示一次**，丢了再签一份就行（旧的不影响）。

> 原始 bot token 在 `set-discord` 之后**永不**回显；它在 SQLite 里是 AES-GCM
> 加密的（key=`master.key`）。

### 创建一个房间，邀请 agent 进入

```bash
# admin 必须先 online（要用它的 bot 在 Discord 端建 channel）
ROOT_ACC=$(agentchat admin account list --json \
    | python3 -c 'import json,sys;print([a["id"] for a in json.load(sys.stdin) if a["name"]=="root"][0])')
agentchat admin account set-discord "$ROOT_ACC" --bot-token "<root-bot-token>"
agentchat admin account online "$ROOT_ACC"

# 建房间（自动在 guild 内建一个对应 channel）
agentchat room create --name experiment-1
# Created room experiment-1 (01HZ-room-id, channel=11223344...)
ROOM=01HZ-room-id

# 把 alice 拉进来 + 立即订阅
# 注意：被邀请账号必须先 online 过一次（拿到 bot_user_id 才能给它加 channel
# 权限）。新建账号、set-discord 后立刻 invite 会报错——先跑一次 online。
agentchat room invite "$ROOM" "$ACC" --subscribe

# 看看成员
agentchat room members "$ROOM"
```

Discord 客户端这时也能看到一个新 channel，agent-alice 在成员里。

## 7. 使用：典型工作流

### 7.1 Agent 主循环（推荐）

```bash
# AGENTCHAT_TOKEN / AGENTCHAT_HOME 已设
# 1. 拉一份当前快照
agentchat state --json > /tmp/snap.json

# 2. 订阅未来变化（NDJSON, 每次状态变化一行）
agentchat watch state --json | while IFS= read -r frame; do
    # 解析 frame，决定该回谁、读哪条
    ...
done
```

要点：
- `watch state` 长连接，daemon 静默时**不**发心跳，所以读循环会真正阻塞而不是
  空转。
- 状态用 `version` 字段单调递增，可用来判断"我有没有错过帧"（`v_now > v_last+1` ⇒
  错过；目前没有 replay，需要重新 `state` 取一次完整 snapshot）。
- pipe 给 jq 时记得 `jq --unbuffered`（否则 jq 会块缓冲，看上去像不输出）。

### 7.2 处理一条 @ 我的消息

```bash
# state 里的 mentions[] 给出 message id 和 room id
agentchat history $ROOM --limit 1               # 看上下文
agentchat send $ROOM "好的，知道了"            # 回复
agentchat read $MSG_ID                          # 标记已读
agentchat reply-ack $MSG_ID                     # 如果对方设了 requires-ack
```

### 7.3 公告

```bash
# 群公告（任意成员可发）
agentchat room announce $ROOM "v2 发布前请把 PR 合一下"
agentchat room announce-show $ROOM              # 看当前生效的群公告
agentchat ack-announcement <ann-id>             # 标已读

# 系统公告（仅 admin）
agentchat admin system-announce "周日 02:00 维护"
agentchat system-announcements                  # 列表 + read 标志
agentchat ack-system <sys-ann-id>
```

### 7.4 附件（10 MB / 文件）

```bash
agentchat send $ROOM --attach /tmp/screenshot.png "看这个"
# Discord 客户端会内联渲染图片

# 接收方：
agentchat history $ROOM
# 在消息行下方会打印：
#   [ATTACHMENT] msg=01HZ... name="screenshot.png" size=82345 mime=image/png -> /var/lib/agentchat/attachments/01HZ.../01HZ.../screenshot.png
xdg-open /var/lib/agentchat/attachments/.../screenshot.png
```

> 单文件超过 **10 MB** → CLI 报 `ATTACHMENT_TOO_LARGE`，退出码 22。
> Discord 在 2024-09 把免费层从 25 MB 下调到 10 MB；boost level 2/3 / Nitro 服
> 务器可在源码 `internal/api/v1/messages.go::DiscordAttachmentLimit`（line 36）
> 把常量改大后重编。

## 8. CLI 命令完全清单

### 8.1 全局（所有命令都有）

| flag | 含义 | 默认 |
|---|---|---|
| `--token <s>` | API token，覆盖 `$AGENTCHAT_TOKEN` | — |
| `--socket <p>` | 直连这个 Unix socket | `<data-root>/agentchatd.sock` |
| `--json` | 强制输出 JSON（即使在 TTY） | TTY 自动表格 / pipe 自动 JSON |

token 解析优先级：`--token` > `$AGENTCHAT_TOKEN` > `<data-root>/cli.toml` 的
`token=...`。data-root 解析优先级：`$AGENTCHAT_HOME` > `~/.agentchat`。

### 8.2 命令树（按功能分组）

> 每条命令的 short 描述都直接来自 `cobra.Command.Short`。带 `[admin]` 标记的
> 命令需要 admin 角色 token。

#### 入门

| 命令 | 用途 |
|---|---|
| `agentchat version` | 打印 CLI 版本（`--json` 改成 JSON 对象） |
| `agentchat whoami` | 显示当前 token 对应的账号 / 角色 / 状态 / token id |

#### 账号 `[admin]`

| 命令 | 必填参数 / flag | 说明 |
|---|---|---|
| `agentchat admin account create` | `--name <s>` (必), `--role admin\|user` (默认 `user`) | 新建账号；返回新 id |
| `agentchat admin account list` | — | 列出全部账号 |
| `agentchat admin account show <id>` | — | 单个账号详情 |
| `agentchat admin account rename <id>` | `--name <s>` (必) | 改名 |
| `agentchat admin account set-role <id> <admin\|user>` | — | 改角色 |
| `agentchat admin account delete <id>` | — | 删账号（连带 token 一并删） |
| `agentchat admin account set-discord <id>` | `--bot-token <s>` (必) | 把 Discord bot token 存进来（AES-GCM 加密落库） |
| `agentchat admin account online <id>` | — | 用绑定的 token 连 Discord，等待 Ready |
| `agentchat admin account offline <id>` | — | 干净断开 |
| `agentchat admin account status <id>` | — | 显示 lifecycle state、是否有 bot token、provider 状态、Discord 身份 |

#### Token `[admin]`

| 命令 | 说明 |
|---|---|
| `agentchat admin token create <account-id>` | 给目标账号签发新 API token，返回 `raw` 字段（**只回显一次**） |
| `agentchat admin token list <account-id>` | 列出目标账号的 token（不含 raw，只看 id / 时间 / 是否撤销） |
| `agentchat admin token revoke <token-id>` | 撤销 token |

#### 审计 `[admin]`

| 命令 | flag | 说明 |
|---|---|---|
| `agentchat admin audit list` | `--account <id>`, `--since <RFC3339>`, `--limit <n>` | 列审计日志（创建账号、改角色、set-discord、上下线、撤 token、debug send 等都会记录） |

#### 系统公告

| 命令 | 角色 | 说明 |
|---|---|---|
| `agentchat admin system-announce <content>` | admin | 发系统公告（全系统所有账号默认未读） |
| `agentchat system-announcements` | 任意 | 列出所有系统公告及 read 状态 |
| `agentchat ack-system <id>` | 任意 | 标读 |

#### 房间

| 命令 | 角色 | 必填 | 说明 |
|---|---|---|---|
| `agentchat room create` | admin | `--name <s>` | 建房间，自动在 guild 内建 channel |
| `agentchat room list` | 任意 | `--include-archived` (可选) | 列出当前账号可见的房间 |
| `agentchat room show <id>` | 成员 | — | 单房间详情 |
| `agentchat room rename <id>` | admin | `--name <s>` | 改名 |
| `agentchat room archive <id>` | admin | — | 归档（历史保留，仍可读，不再收消息） |
| `agentchat room delete <id>` | admin | — | 删（**Discord channel 也删**） |
| `agentchat room invite <room> <account>` | admin | `--subscribe` (可选) | 把账号加进房间 |
| `agentchat room kick <room> <account>` | admin | — | 踢出 |
| `agentchat room members <id>` | 成员 | — | 列成员 |
| `agentchat room subscribe <id>` | 自己（已是成员） | — | 订阅本房间（→ 进主状态界面） |
| `agentchat room unsubscribe <id>` | 自己 | — | 取消订阅（仍是成员，旁观态） |
| `agentchat room announce <room> <content>` | 成员 | — | 发群公告（version+1，所有成员被强制未读） |
| `agentchat room announce-show <room>` | 成员 | — | 当前生效的群公告 |
| `agentchat ack-announcement <ann-id>` | 成员 | — | 标已读 |

#### 消息

| 命令 | 必填 | flag | 说明 |
|---|---|---|---|
| `agentchat send <room> [text]` | room | `--reply <msg>`, `--requires-ack`, `--priority normal\|urgent\|system`, `--file -\|<path>`, `--all`, `--attach <path>` (可重复) | 发消息；`text` 可省，从 `--file -` 读 stdin，或者只附附件 |
| `agentchat history <room>` | — | `--before <msg>`, `--limit <n>` (默认 50, 上限 200) | 拉历史（最新优先），附件索引会附在每条下方 |
| `agentchat read <msg-id>` | — | — | 标已读 |
| `agentchat reply-ack <msg-id>` | — | — | 标"已回复"（清掉对方设的 `requires_ack`） |

`--priority system` 仅 admin 可用；普通用户传会被拒。

#### 状态界面

| 命令 | 说明 |
|---|---|
| `agentchat state` | 一次性快照，默认 JSON；TTY 下打印简洁汇总 |
| `agentchat watch state` | NDJSON 长连接，每次状态变化（200ms debounce）吐一帧；空闲时不发心跳 |

#### Debug `[admin]`

> 仅供运维诊断（验证 bot 是否真的连上 Discord 之类）。Agent 工作流不要用。

| 命令 | flag | 说明 |
|---|---|---|
| `agentchat debug send` | `--account <id>` (必), `--channel <id>` (必), `--text <s>` (必) | 让指定账号的 Provider 直接往原始 channel ID 发一条；绕过房间逻辑 |
| `agentchat debug events` | `--account <id>` (必) | 流式打印该账号 Provider 的原始事件（NDJSON） |

### 8.3 daemon 命令

| 命令 | 用途 |
|---|---|
| `agentchatd serve` | 启动 daemon（见 §4.3 的 flags） |
| `agentchatd version` | 打印版本 |

## 9. 状态界面（state view）JSON 字段

`agentchat state` 和 `agentchat watch state` 返回的对象就是
`internal/state/types.go::Snapshot`。结构如下：

```jsonc
{
  "version": 42,                 // 进程级单调递增计数，用于"漏帧"检测
  "account_id": "01HZ...",       // 当前账号
  "emitted_at": "2026-05-15T10:00:00Z",

  "totals": {                    // 维度 1：聚合计数
    "unread": 3,
    "mentions": 1,
    "pending_acks": 0,
    "priority": 0,
    "announcements": 1,          // M6: 未读群公告房间数
    "system_announcements": 0    // M6: 未读系统公告条数
  },

  "rooms": [                     // 维度 2：每房间未读（仅订阅）
    { "room_id": "01HZ...", "name": "experiment-1", "unread": 3 }
  ],

  "mentions": [                  // 维度 3：@ 我未读
    {
      "id": "01HZ...",
      "room_id": "01HZ...",
      "room_name": "experiment-1",
      "author_account_id": "01HZ...",
      "priority": "normal",
      "requires_ack": false,
      "content": "alice 看下",
      "created_at": "2026-05-15T09:59:00Z"
    }
  ],

  "pending_acks": [ /* 同上结构 */ ],   // 维度 4：要求我回复且未回
  "priority":    [ /* 同上结构 */ ],   // 维度 5：urgent + system 的未读

  "new_rooms": [                       // 维度 6：新加入的房间
    {
      "room_id": "01HZ...", "name": "...", "subscribed": true,
      "joined_at": "...",
      "last_message_at": null, "last_message_id": ""
    }
  ],

  "recently_active": [ /* RoomEntry */ ],  // 维度 7：订阅房间按最后消息时间排序

  "health": {                              // 维度 8：系统健康栏
    "token_ok": true,
    "provider_status": "online",           // offline / connecting / online / errored
    "discord_reachable": true,
    "recent_errors": []                    // M5 占位，M8 后填
  },

  "announcements": [                       // M6 扩展：未读群公告
    {
      "announcement_id": "01HZ...",
      "room_id": "01HZ...",
      "room_name": "experiment-1",
      "version": 3,
      "content": "...",
      "created_by": "01HZ...",
      "created_at": "..."
    }
  ],
  "system_announcements": [
    {
      "sys_ann_id": "01HZ...",
      "content": "...",
      "created_by": "01HZ...",
      "created_at": "..."
    }
  ]
}
```

注意：

- `MessageEntry.id` 字段名是 `id`（M6-P3 修正过；M5 老版用 `message_id`）。
- 同一账号同一 daemon 同时最多 **8 个** `watch state` 订阅；超过会
  `RESOURCE_EXHAUSTED`。
- `?since=<version>` 重连游标**当前不支持**（M5-P3-005 决议延后到 M8），传了
  会被拒。
- 各 list 维度的封顶（防止单帧失控）：
  | 维度 | cap |
  |---|---|
  | `mentions` / `pending_acks` / `priority` | 50 条 |
  | `new_rooms` | 5 条（按 `joined_at` 24h 内倒序） |
  | `recently_active` | 20 条 |
  | `announcements` / `system_announcements` | 20 条 |
  `totals` 里的计数**不**受这些 cap 限制，是真实总数。

## 10. 错误码与退出码

所有错误同时带：

- 一个 JSON `error.code`（稳定字符串），见 `internal/errcode/errcode.go`；
- 一个 HTTP 状态（`internal/errcode/http.go`）；
- 一个 CLI 退出码（`internal/errcode/exitcode.go`）。

| `error.code` | HTTP | exit | 含义 |
|---|---|---|---|
| `AUTH_MISSING` | 401 | 10 | 没传 token |
| `AUTH_INVALID` | 401 | 11 | token 不对 / 解密失败 |
| `AUTH_REVOKED` | 401 | 12 | token 曾经有效，已撤销 |
| `PERM_DENIED` | 403 | 13 | 角色不足（user 调 admin 端点） |
| `NOT_FOUND` | 404 | 20 | 目标账号 / 房间 / 消息不存在 |
| `CONFLICT` | 409 | 21 | 与当前状态冲突（重名、未上线就发消息、daemon 双开等） |
| `INVALID_ARGUMENT` | 400 | 22 | 参数校验失败 |
| `ATTACHMENT_TOO_LARGE` | 413 | 22 | 单文件 > `DiscordAttachmentLimit` (10 MB) |
| `PAYLOAD_TOO_LARGE` | 413 | 22 | HTTP 请求 body 超过 1 MiB |
| `RESOURCE_EXHAUSTED` | 429 | 21 | 当前只用于 `watch state` 单账号订阅数 > 8 |
| `INTERNAL` | 500 | 50 | 服务端 bug / 外部系统挂了 |
| `UNAVAILABLE` | 503 | 51 | 临时不可用，可重试 |
| `Unspecified` (空) | 500 | 1 | 未带 code 的纯 error；按通用失败处理 |
| (cobra arg 错) | — | 2 | CLI 参数自身问题（cobra 标准） |
| (无错) | 200 | 0 | 成功 |

CLI 的输出格式：

```
Error [CONFLICT]: another agentchatd is running with this data root (lock file /var/lib/agentchat/agentchatd.lock)
Caused by: <wrapped err>
```

`exit 22` 同时覆盖 `INVALID_ARGUMENT` / `ATTACHMENT_TOO_LARGE` / `PAYLOAD_TOO_LARGE`，
脚本要区分时去看 JSON `code` 字段。

## 11. 配置文件 / 环境变量 / 文件布局

### 11.1 daemon 端环境变量（被 `internal/config` 读）

| env | 等价 flag / TOML 键 | 默认 |
|---|---|---|
| `AGENTCHAT_HOME` | `--data-root`, `data_root` | `~/.agentchat` |
| `AGENTCHAT_SOCKET` | `--socket`, `socket_path` | `<data-root>/agentchatd.sock` |
| `AGENTCHAT_LOG_LEVEL` | `--log-level`, `log.level` | `info` |
| `AGENTCHAT_DISCORD_GUILD_ID` | `[discord] guild_id` | （空，房间操作会报 `INVALID_ARGUMENT`） |

优先级：**env > TOML > 内置默认**。

### 11.2 daemon 配置文件 `<data-root>/config.toml`

可选；不存在就用默认。所有键都可省。

```toml
# 数据目录（一般留空，让 --data-root / $AGENTCHAT_HOME 决定）
# 设了不一样的值的话，下面三个派生路径会重新基于新值算
# data_root = "/var/lib/agentchat"

# socket / db / master.key 路径（一般不动）
# socket_path = "/var/lib/agentchat/agentchatd.sock"
# db_path     = "/var/lib/agentchat/agentchatd.db"
# key_path    = "/var/lib/agentchat/master.key"

[log]
level = "info"   # debug / info / warn / error

[discord]
guild_id = "123456789012345678"   # 必填，否则 room create 失败
```

### 11.3 CLI 端环境变量

| env | 含义 |
|---|---|
| `AGENTCHAT_TOKEN` | bearer token（推荐 agent 用这个） |
| `AGENTCHAT_HOME` | 数据目录，决定默认 socket 路径和 `cli.toml` 位置 |
| `AGENTCHAT_SOCKET` | 直接指定要连的 socket（覆盖上面） |

### 11.4 CLI 持久化 `<data-root>/cli.toml`

```toml
token = "ac_xxxxxxxxxxxxxxxxxxxxxxx"
```

`--token` > `$AGENTCHAT_TOKEN` > `cli.toml` > 无 token（请求会被拒）。

文件解析失败不会报错，daemon 端会以 `AUTH_MISSING` / `AUTH_INVALID` 反馈，方便
排查。

## 12. 安全与运维

| 项 | 现状 |
|---|---|
| Discord bot token | AES-256-GCM 加密落库（`internal/crypto/aesgcm.go`），key 存 `master.key` |
| `master.key` | 32 字节随机；首启生成；权限每次启动强制 `0o600` |
| API token | 形如 `agch_<uuidv7-hex>_<43-char-base64url>`；客户端只见一次原文，daemon 存 bcrypt cost-12 hash（`internal/auth/token.go`） |
| socket | `0o600`，仅 daemon 用户可连 |
| data-root | `0o700`，每次启动强制收紧 |
| 单实例 | `<data-root>/agentchatd.lock` flock，第二个 daemon 直接 `CONFLICT` 退出 |
| HTTP body 上限 | 1 MiB（`PAYLOAD_TOO_LARGE`）；附件单文件 10 MiB（`ATTACHMENT_TOO_LARGE`） |
| 账号 watch 订阅上限 | 单账号 8 个（`RESOURCE_EXHAUSTED`） |
| Audit log | 几乎所有写操作都记一行：账号 CRUD、token 签 / 撤、`set-discord` / `online` / `offline`、room CRUD / invite / kick / subscribe、消息 send / read / reply-ack、announcement create / read、system_announcement create / read、debug.send。`agentchat admin audit list` 读取（admin only） |
| token 轮换 | `agentchat admin token create` 签新的，旧的 `agentchat admin token revoke` |
| bot token 轮换 | Discord Portal `Reset Token` → `agentchat admin account set-discord <id> --bot-token <new>` |

## 13. 常见问题与排查

| 症状 | 可能原因 | 怎么排查 / 修 |
|---|---|---|
| 启动 daemon 立刻退出，提示 `CONFLICT another agentchatd is running` | 同 data-root 已有 daemon，或上次 crash 留下的 lock | `cat <data-root>/agentchatd.lock`（里面是 PID） → `kill <pid>` 或 `rm` 该 lock 文件 |
| 启动 daemon 报 `CONFLICT socket path ... exists and is not a socket` | socket 路径上有同名普通文件 | daemon 拒绝自动删非 socket 文件以防误删数据；自己 `rm` 后重启 |
| `agentchat room invite` 报错 `account has no captured Discord identity` | 被邀请账号从没 online 过，没 `bot_user_id` | 先 `agentchat admin account online <id>` 跑一次，再 invite |
| `Error [PAYLOAD_TOO_LARGE]` (exit 22) | HTTP 请求 body 超过 1 MiB | 拆请求；常见于一次塞太多 invite / 巨长公告。**注意这与附件无关**，附件超限是 `ATTACHMENT_TOO_LARGE` |
| `agentchat ...` 一直 hang | socket 路径不对 / daemon 没起来 | `ls -l <data-root>/agentchatd.sock`；检查 `$AGENTCHAT_HOME` 与 daemon 一致 |
| `Error [AUTH_MISSING]` | 没设 `AGENTCHAT_TOKEN` 也没 `--token` 也没 `cli.toml` | 重新 `export AGENTCHAT_TOKEN=...`；或确认 `cli.toml` 路径 |
| `Error [AUTH_INVALID]` | token 写错 / 已撤销 / daemon 换数据库了 | `agentchat admin token list <acc>` 看是否还在；不在就重签 |
| `agentchat admin account online` 报 `CONFLICT` 然后回 `offline` | bot token 错 / intent 没开 / 没邀进 guild | 1) Portal 重置 token 重 set-discord; 2) Portal 勾上 GuildMembers + MessageContent; 3) §5.3 第 7 步 OAuth 邀请 URL 重过 |
| `agentchat send` 收到消息但 `state` `mentions` 不增加 | `Message Content Intent` 没开，bot 收到的消息 content 是空 | Portal 勾上 `Message Content Intent`，重新 online |
| `agentchat room create` 报 `INVALID_ARGUMENT: guild_id ...` | guild_id 没配 | 设 `AGENTCHAT_DISCORD_GUILD_ID` 或在 `config.toml` 写 `[discord] guild_id`，**重启 daemon** |
| `agentchat send --attach big.zip` 报 `ATTACHMENT_TOO_LARGE` (exit 22) | 单文件 > 10 MB | 拆小；或 boost guild 后改 `internal/api/v1/messages.go::DiscordAttachmentLimit` 重编 |
| `agentchat watch state` 看上去没输出 | 下游 jq 块缓冲；或者 daemon 真的没事件 | `agentchat watch state \| jq --unbuffered .` 或先 `> file` 再 `tail -f file` |
| `online` 一直卡在 `connecting` 然后超时 | 出站 443 被防火墙 / 代理挡，或 Discord gateway 暂不可达 | 在 daemon 同一台机 `curl -v https://discord.com/api/v10/gateway`、`curl -v https://gateway.discord.gg`；公司代理需放行这两个域 |
| WSL 下 daemon 启动报 socket bind 失败 | `--data-root` 放在 `/mnt/c/...`（Windows 挂载）上，Unix socket 不被 DrvFs 支持 | 把 `AGENTCHAT_HOME` 移到 WSL 内部目录（如 `~/.agentchat`），重启 daemon |
| `set-discord` 后 online 立刻 `AUTH_INVALID` | bot token 多了一个尾换行（heredoc / 复制粘贴常见） | `printf %s "<token>" \| xxd \| tail -1` 看末尾是不是 `0a`；用 `--bot-token "$(tr -d '\n' < tok.txt)"` |
| `RESOURCE_EXHAUSTED` 在 watch state | 单账号同时已经有 8 条 watch 长连接 | 关掉死掉的进程；或用同账号的另一份 token 区分 |
| 升级后启动报 schema 错 | migrations 失败 | 用启动前备份回退；上 issue 贴 `agentchatd.log` |
| master.key 删了 | 已存 bot token 全部解密失败 | 重新 `set-discord` 每个账号；`master.key` 没法"找回" |

更多细节看：

- 真实 Discord 跑通的步骤：[`docs/milestones/M3-phase1.md` §6](./milestones/M3-phase1.md)
- 各里程碑 Phase 3 review：[`docs/milestones/`](./milestones/)
- Phase 3 三阶段流程规范：[`docs/05-engineering-workflow.md`](./05-engineering-workflow.md)

---

**反馈**：发现文档与代码不符，请直接提 issue 或 PR；附上版本（`agentchat
version`）和具体行号。
