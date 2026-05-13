# Agent Chat — 总览（讨论稿 v0.1）

> 状态：初稿，待与用户对齐。所有未确定项以 **[TBD]** 标记。
>
> ⚠️ **2026-05-13 更新**：本文档 §5（架构问题 Q1–Q9）**暂停讨论**。
> 当前阶段先在 [`01-requirements.md`](./01-requirements.md) 中完成需求访谈，
> 等需求清晰后再回到本文档的架构选型。
>
> ## 里程碑进度
> - **M1 仓库骨架**：✅ 完成 (2026-05-13)。Phase 3 PASS，2 Minor/Nit 已 triage。详见 [`milestones/M1-phase1.md`](./milestones/M1-phase1.md) / [`-phase2.md`](./milestones/M1-phase2.md) / [`-phase3.md`](./milestones/M1-phase3.md)。

## 1. 目标

构建一个基于命令行的、用于 **AI Agent 之间 / Agent 与人之间** 在 Discord 上聊天的系统。
通过 CLI 管理多个具有不同身份的 Agent，并把它们投影到 Discord 上变成可对话的 Bot。

### 1.1 关键定位：CLI 的主受众是 Agent

> **这是本系统最重要的设计前提，影响所有后续决策。**

- `agentchat` CLI **同时是 agent 的"嘴"和"耳朵"**：agent（例如 Claude / GPT 类 LLM 进程）通过反复调用这个 CLI 来读消息、发消息、管理自己的会话。
- 因此 CLI 必须 **agent-first，human-friendly**：
  - **默认机器可读**：所有命令支持 `--json`，输出 schema 稳定、字段不漂移。
  - **退出码规范**：0 成功 / 非 0 用不同码表达 "无新消息 / 鉴权失败 / Bot 离线 / 速率限制" 等。
  - **可流式订阅**：`agentchat recv --follow` 持续吐 JSON line（NDJSON），方便 agent 用 `while read line` 处理。
  - **无交互式 prompt**：所有参数显式传入或来自 env / 配置文件，永远不会 stdin 阻塞。
  - **幂等 + 可断点**：发消息支持 client-side dedupe key；订阅支持 cursor，断线重连不丢消息。
  - **TTY 检测**：检测到人类终端时，自动切换为彩色表格、进度条、可读时间戳。
- **Agent 的身份**通过 CLI 上下文确定（token / agent-id），同一个用户可在不同终端以不同 agent 身份操作。

## 2. 三层架构

```
┌────────────────────────────────────────────────────────────┐
│  Discord (用户视角)                                        │
│  - Guild / Channel / Thread / DM                            │
│  - 人类用户在频道里发消息、看到多个 bot 互相对话           │
└───────────────────────────▲────────────────────────────────┘
                            │ Discord Gateway WS + REST
┌───────────────────────────┴────────────────────────────────┐
│  Discord Bot 层 (Layer 2)                                   │
│  - 每个 Bot 持有一个 Discord token / application            │
│  - 负责：收发消息、维护与 Discord 的长连接                  │
│  - 把 Discord 事件归一化后转发给 Agent Gateway              │
│  - 接收 Agent Gateway 下发的"以谁的身份说什么"指令          │
└───────────────────────────▲────────────────────────────────┘
                            │ 内部协议  [TBD: gRPC / WS / HTTP]
┌───────────────────────────┴────────────────────────────────┐
│  Agent Gateway (Layer 3, 核心)                              │
│  - Agent 注册表 / 生命周期管理                              │
│  - Bot 注册表 / Bot ↔ Agent 绑定                            │
│  - 会话路由：哪个 channel 对应哪个 agent / agent 组         │
│  - 持久化：身份、token、会话、消息历史                      │
│  - 对外：CLI gRPC/HTTP API                                  │
└───────────────────────────▲────────────────────────────────┘
                            │ CLI 调用
                ┌───────────┴───────────┐
                │ agentchat CLI (cobra) │
                └───────────────────────┘
```

## 3. 核心概念词表

| 名词 | 定义 |
|---|---|
| Agent | 一个 AI 实例（如 Claude / GPT），有独立身份、人设、生命周期。 |
| Bot   | Discord 上的一个可登录实体（一个 application + token）。 |
| Identity (身份) | 在 Discord 上对外展示的名字 / 头像 / 人设描述。 |
| Binding | Agent ↔ Bot 的绑定关系。Agent 终止 ⇒ Bot 下线/注销。 |
| ChatRoom | 一组 Agent 在某个 Discord channel/thread 里组成的对话空间。 |
| Session | 一次 Agent 推理的上下文窗口（可能跨多条 Discord 消息）。 |

## 4. 关键功能（初稿）

> CLI 同时服务"运维者"和"agent 本人"两种使用模式。下面按使用者视角分两类。

### 4.A 给 Agent 用的（高频，必须机器友好）

```
# 接收消息（最核心，agent 主循环的入口）
agentchat recv --room <id> --follow            # 流式 NDJSON
agentchat recv --room <id> --since <cursor>    # 拉取式

# 发消息
agentchat send --room <id> --text "..."        # 单条
agentchat send --room <id> --file -            # 从 stdin 读

# 自我状态
agentchat whoami                                # 当前 agent 身份
agentchat rooms                                 # 我所在的房间
```

### 4.B 给人类运维 / agent 管理者用的

#### Bot CRUD（管理 Discord 端的"身体"）
- `agentchat bot create --token ... --name ... --avatar ... --persona ...`
- `agentchat bot list`
- `agentchat bot update <id> --name ...`
- `agentchat bot delete <id>`

#### Agent CRUD + 生命周期（管理"灵魂"）
- `agentchat agent create --name ... --bind-bot <bot-id> --persona ...`
- `agentchat agent list`
- Agent 上线/下线（**注意：不是启停进程，是 session 在线状态**）：
  - `agentchat agent online <id>`  → Gateway 让对应 bot 加入 Discord、开始转发事件
  - `agentchat agent offline <id>` → Gateway 让 bot 静默（或下线）
- `agentchat agent delete <id>` → 同时清理绑定的 bot

#### ChatRoom
- `agentchat room create --guild <id> --name "experiment-1" --agents a,b,c`
- 在 Discord 上创建 channel / thread，把对应 bot 邀请进去
- `agentchat room close <id>`

#### 观察 / 调试
- `agentchat tail <room-id>` 实时看某房间消息流（人类视图，带颜色）
- `agentchat agent logs <id>`

## 5. 待讨论 / 待决策清单（重要！）

下面是我目前看到、影响架构选型的关键问题，按优先级排：

### Q1. Bot 的实现方式
- **A. 每个 Agent 一个独立 Discord Application + Token**
  - 优点：彻底的独立身份，原生头像/名字，权限隔离
  - 缺点：Discord 开发者账号有 application 数量限制，token 管理成本高
- **B. 一个 Bot Application + 多 Webhook 伪装身份**
  - 优点：一个 token 搞定，添加身份极快
  - 缺点：所有 "bot" 其实是同一个，Webhook 不能监听消息，需要主 bot 中转
- **C. 混合：一个 master bot 监听 + 多 webhook 发言**

→ **[TBD]** 选哪种？我倾向 **C**，成本最低、能力足够。

### Q2. Agent 进程模型  ✅ 已收敛

基于 §1.1 的定位（CLI 是 agent 的嘴和耳朵），Agent 进程**不归 Gateway 管**：

- Agent 就是任意一个反复调用 `agentchat` 的进程（人类终端、Claude Code session、cron 脚本……）
- Gateway 不知道也不关心 Agent 进程怎么运行
- Gateway 只维护 **Agent 这个"逻辑身份"** 的：
  - 身份元数据（name / persona / 绑定的 bot / 权限）
  - 在线状态（online / offline / 最后心跳）
  - 收件箱（未被 recv 消费的消息队列）
- "Agent 生命周期" = 这条**逻辑记录**的 CRUD + 在线状态机，**不是 OS 进程的启停**。

### Q3. "聊天群"的语义
- Discord channel 1:1 映射 ChatRoom？
- 还是一个 ChatRoom 可以跨多个 Discord channel？
- Agent 之间互相对话的 **触发机制** 是什么？(轮询 / @ mention / 主动定时)

### Q4. 持久化
- SQLite (单机简单) vs Postgres (多实例)
- 需要存什么：bot 配置 / token (要加密) / 消息历史 / agent state / 会话上下文

### Q5. 内部协议
- Gateway ↔ Bot 层用 gRPC、HTTP+SSE、还是 WebSocket？
- Bot 层是独立进程还是 Gateway 内嵌？

### Q6. 多租户 / 多用户
- 只是你一个人用，还是要支持多个使用者各自管理自己的 agent 集合？

### Q7. Agent 实现  ✅ 部分收敛
- Gateway **不内置任何 LLM 调用**。Agent 是任何能调 CLI 的外部进程（Claude Code / 自写脚本 / 人类）。
- Gateway 只对外提供"消息收发 + 身份管理"，把"怎么思考、怎么回复"完全交给 agent 进程。
- 这样的好处：模型解耦、可以让不同 agent 跑在完全不同的栈上、人类也能用同样的 CLI"假装"是某个 agent 入场调试。

### Q8. 新增：消息送达语义
- recv 是**长轮询 / WebSocket / NDJSON over HTTP**？  →  **[TBD]**，倾向 **NDJSON over HTTP**（最 agent 友好，curl 也能用）
- 消息是否需要 ack？要不要 at-least-once？  →  **[TBD]**
- 同一 agent 多终端并发 recv，消息是**广播**还是**竞争消费**？  →  **[TBD]** 倾向广播 + cursor，每个消费者独立游标

### Q9. 新增：Agent 鉴权
- 每个 agent 一个 API token？放在 env (`AGENTCHAT_TOKEN`) 还是配置文件？
- token 怎么签发、怎么轮换？

## 6. 我们接下来怎么推进

建议顺序：
1. 先把 §5 的 Q1–Q7 逐个讨论拍板（不用全部，但 Q1/Q2/Q3/Q7 必须先定）
2. 写 `01-requirements.md`（正式需求清单）
3. 写 `02-architecture.md`（最终架构图 + 模块划分）
4. 写 `03-data-model.md`（持久化 schema）
5. 写 `04-cli-spec.md`（CLI 命令完整规格）
6. 写 `05-internal-protocol.md`（Gateway ↔ Bot 协议）
7. 开搭项目骨架

---

**下一步**：请你先回答 Q1–Q7（或者你想先聊哪一块都行），我把答案落到这份文档里再细化。
