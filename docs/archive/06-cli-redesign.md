# CLI + Discord @ Redesign — two-verb plan (M9)

> 状态：**设计稿**，未实施。基线：v0.0.0 已发布。本文档定稿后再动
> `internal/api/v1/messages.go` / `internal/bot/discord/discord.go` /
> `cmd/agentchat/cmds/` / SQLite migrations。
>
> 目标：彻底贴合真实 IM 心智模型，把 agentchat 自己发明的 "requires_ack /
> reply_ack / --all / mention_all" 工程概念全部废除，统一走 **Discord
> 原生 @ 系统**。命令空间收敛为 **`read <room>` + `send <room>`** 两个
> 核心 verb。

---

## 1. Motivation

### 1.1 当前症结

agentchat 的 mention / ack 子系统跟 Discord **完全没有连通**：

| 维度 | 现状 | 问题 |
|---|---|---|
| Discord 入站 `m.Mentions` | `discordToMessage` 完全丢弃 | 人在 Discord 客户端 `@alice` → alice 这个 agent 永远看不到这个信号 |
| Discord 入站 `m.MentionEveryone` | 丢弃 | 真人发 `@everyone` agent 不感知 |
| agentchat 出站 | `SendMessage` 不构造 `<@id>` 也不设 `AllowedMentions` | `--all` 只改 SQLite 不真 ping，Discord 客户端那侧"群发"无声 |
| `requires_ack` / `reply_ack` | 工程自创的"必须回复"语义 | 真实 IM 没有；@ 已经天然带"要处理"含义 |
| `--all` / `mention_all` | 内部标记不发真 ping | 跟 Discord 完全脱节，两套 mention 系统并存 |

观察到的不健康信号：

- agent 主循环要走 5 步：`state → history → 决策 → send → read → reply-ack`，每步都暴露中间 state
- "标已读 / 标回复"作为 verb 出现在 CLI——这是 GET-then-PATCH 的工程思维泄漏
- 真实 IM 用户从来不需要"手动标已读"按钮；mention = "要看一下"，看了 = 处理完
- state 视图里 `unread` / `mentions` / `pending_acks` 三个维度互相重叠

### 1.2 设计哲学

- **两个核心动作对应两个核心 verb**：看 / 写
- **被 @ = 要处理**——合并 mention 和原 pending_ack 两个 state 维度
- **看了 = 处理了**——`read <room>` 自动标已读，没有显式"已回复"概念
- **@ 系统**直接走 Discord 原生 mention，不在 agentchat 这一层再发明
- **副作用绑定在 verb 上**——读 = 浏览 + 标读；不暴露中间 state 给 CLI
- **砍掉的命令 / 字段不保留兼容**。v0.0.x 阶段没有外部依赖

---

## 2. 目标设计

### 2.1 两个 verb

```bash
agentchat read <room>      # 看（未读 + 上下文 + 自动标已读 + 附件索引）
agentchat send <room>      # 写（正文里写 @<name> 即 Discord 原生 mention）
```

### 2.2 砍掉的命令 / flag / 概念

| 旧概念 | 处置 | 替代 |
|---|---|---|
| `agentchat history <room>` | **删** | `agentchat read <room> --before <msg> --limit N` |
| `agentchat read <msg-id>` | **删** | `agentchat read <room>`（覆盖该消息） |
| `agentchat reply-ack <msg-id>` | **删** | 不需要——@ 我的消息看了就算处理 |
| `send --requires-ack` | **删** | 想要对方处理 → 在正文里 `@<name>` |
| `send --all` / `mention_all` | **删** | 想全员看 → 在正文里 `@everyone`，走 Discord 原生 |
| `messages.requires_ack` 列 | **migration 删除** | — |
| `message_states.replied_at` 列 | **migration 删除** | — |
| `messages.mention_all` 列 | **migration 删除** | — |
| state.totals.pending_acks | **删** | — |
| state.pending_acks[] | **删** | — |

### 2.3 新增

| 项 | 说明 |
|---|---|
| `message_mentions(message_id, account_id)` 表 | per-message 多对多 mention 关系 |
| `accounts.name` `UNIQUE` 约束 | 让 `@<name>` 解析无歧义 |
| Discord 入站 mention 解析 | `discordToMessage` 读 `m.Mentions[]` + `m.MentionEveryone` |
| Discord 出站 mention 注入 | daemon 解析正文里 `@<name>` 字面量 → 翻译成 `<@bot_user_id>` + 设 `AllowedMentions` |
| bot username = account name 强制一致 | `set-discord` 时校验，不一致拒绝 |

---

## 3. `read <room>` 详细规格

### 3.1 表面

```bash
agentchat read <room-id> [--limit N] [--before <msg-id>] [--json]
```

| 参数 | 类型 | 默认 | 含义 |
|---|---|---|---|
| `<room-id>` | 位置参数 | 必填 | 目标房间 |
| `--limit N` | int | **10** | 上下文条数；未读不受此限制 |
| `--before <msg-id>` | string | 空 | 翻老历史的游标 |
| `--json` | bool | TTY 默认 false / pipe 默认 true | 强制 JSON 输出 |

### 3.2 默认行为（不带 `--before`）

1. 取该 room **当前所有未读**（上限 200，超过返回 `more=true`）
2. 取该 room **最近 N 条已读历史**（默认 10）作为上下文
3. 合并按时间顺序（旧 → 新）一次返回
4. 在**同一事务**内对未读消息批量 `MessageState.Upsert(ReadAt=now)`
5. 事务提交后 `state.Bus.Publish(actor.ID)` 只发一次

### 3.3 `--before` 翻页行为

带 `--before` = **纯查询模式，不标已读**。

| 调用 | 返回 | 副作用 |
|---|---|---|
| `read <room>` | 未读全部 + 最近 10 条已读 | **批量标未读为已读** |
| `read <room> --limit 30` | 未读全部 + 最近 30 条已读 | **批量标未读为已读** |
| `read <room> --before <m>` | `<m>` 之前 50 条（默认） | **不动** |
| `read <room> --before <m> --limit 100` | `<m>` 之前 100 条 | **不动** |

### 3.4 JSON 输出 schema

```jsonc
{
  "room": {
    "id": "01HZ...",
    "name": "experiment-1",
    "subscribed": true,
    "current_announcement_id": "01HZ..."   // 可空
  },
  "marked_read": ["01HZ-msg-1", "01HZ-msg-2"],   // 本次标了哪些；--before 模式下为空
  "messages": [
    {
      "id": "01HZ...",
      "author_account_id": "01HZ...",
      "author_name": "alice",                     // 方便展示
      "content": "<@bot_user_id> 看下",           // 原始 Discord content（含 <@id> 标记）
      "display_content": "@alice 看下",            // daemon 渲染好的字符串（替换 <@id> 为 @name）
      "reply_to": "01HZ...",                       // 可空
      "mentions": ["01HZ-account-id-alice"],       // @ 了哪些 account（未含 @everyone）
      "mention_everyone": false,                   // 是否 @everyone
      "priority": "normal",
      "created_at": "2026-05-15T10:00:00Z",
      "read_at": "2026-05-15T10:01:00Z",
      "attachments": [
        { "id": "...", "filename": "...", "size": 0, "mime": "...", "local_path": "..." }
      ]
    }
  ],
  "more": true   // 是否有更老的消息可以 --before 继续翻
}
```

`content` 字段保留 Discord 原始格式（含 `<@id>`），方便程序匹配；`display_content`
是 daemon 渲染好的人类可读串。Agent 通常只看 `mentions` 数组判断"我有没有被点名"。

### 3.5 TTY 输出（人用）

按时间顺序打印，分两段：

```
=== UNREAD (3) ==========================================
[10:00] alice    @bob @carol 看一下 PR #42
[10:01] bob      OK
[10:02] alice    > bob: 收到

=== CONTEXT (last 10) ===================================
[09:45] alice    早上好
...

✔ marked 3 messages as read
```

---

## 4. `send <room>` 详细规格

### 4.1 表面

```bash
agentchat send <room-id> [text] \
  [--file -|<path>] \
  [--reply <msg-id>] \
  [--priority normal|urgent|system] \
  [--attach <path>]...
```

**没有** `--mention` / `--all` / `--requires-ack` flag。一切 @ 都通过**正文里直接写**实现。

### 4.2 参数

| flag | 行为 |
|---|---|
| `[text]` | 消息正文。正文里的 `@<name>` / `@everyone` 由 daemon 解析 |
| `--file -\|<path>` | 内容来源；正文里的 @ 同样被解析 |
| `--reply <msg-id>` | 回复某条 → Discord 原生 reply（`MessageReference`）|
| `--priority` | `normal` / `urgent` / `system`（仅 admin） |
| `--attach <path>` | 附件（可重复，单文件 ≤ 10 MB） |

### 4.3 正文 @ 解析规则

daemon 在 SendMessage 前扫描 content：

| 模式 | 处理 |
|---|---|
| `@everyone` 字面量 | 设 `AllowedMentions.Parse += [everyone]`；content 保留字面量 |
| `@<name>` 字面量，`<name>` 是该 room 成员的 account.name（且唯一） | 替换为 `<@bot_user_id>`；设 `AllowedMentions.Users += [bot_user_id]` |
| `@<name>` 字面量但 name 不在 room 内 / 不存在 | **保留字面量**，不进 AllowedMentions（不 ping）；跟 Discord 客户端打了无效 @ 行为一致 |
| `@here`、`@<role-id>` 等其它 Discord mention | **不处理**（明确不支持，Q9） |
| `<@123456789>` Discord 原生格式 | **拒绝**（agent 不该手写 bot_user_id）：返回 `INVALID_ARGUMENT` |

`<name>` 字符集：`[a-zA-Z0-9._-]+`（贴 Discord username 实际约束）。

```bash
agentchat send room-xxx "@alice 看下 #42"
# daemon 解析:
#   - 找到 @alice，alice 在该 room，bot_user_id=98765
# Discord 发送:
#   content="<@98765> 看下 #42"
#   AllowedMentions={Users: ["98765"]}
# SQLite 写:
#   messages.content = "<@98765> 看下 #42"
#   message_mentions(msg_id, alice_account_id)

agentchat send room-xxx "@everyone all hands"
# AllowedMentions={Parse: [everyone]}
# SQLite 写:
#   messages.content 保留字面量
#   message_mentions 表为空（@everyone 通过另一列存）

agentchat send room-xxx "看下 @nonexistent 这个怎么处理"
# 没匹配；不 ping，保留字面量
```

### 4.4 `--reply` ↔ Discord 原生 reply

`send --reply <msg-id>` 走 Discord `MessageReference`（已实现，见
`internal/bot/discord/discord.go:217-220`）。message id 即 Discord 原生
message ID（agentchat 用同一个 ID）。

Discord 端会显示"reply to" 引用块，跟人类用户用客户端"回复"按钮的视觉一致。

**Reply 默认 ping 原作者**——保留 Discord 客户端的默认体验。`AllowedMentions`
显式声明 `RepliedUser=true`（虽然不设也是 true，但显式让意图清晰，
不被 AllowedMentions 的"默认全禁"误覆盖；见 §5.2）。

**不**额外把 `<@author>` 注入到 content 正文——reply 这个引用块本身就带
ping 语义，content 维持用户原文。

### 4.5 权限

- 必须是 `<room>` 的成员
- `--priority system` 仅 admin
- 正文里 @ 的对象**必须**是该 room 的成员；不是则当字面量处理
- `@everyone` 需要 Discord bot 在该 channel 有 Mention Everyone permission；
  没权限 Discord 会拒绝，daemon 返回 `UNAVAILABLE`

---

## 5. Discord @ 系统对接

### 5.1 入站（Discord → agentchat）

`discordToMessage` (`internal/bot/discord/discord.go:447`) 改造：

```go
out := &bot.Message{
    ID:        m.ID,
    ChannelID: m.ChannelID,
    AuthorID:  author,
    Content:   m.Content,
    CreatedAt: m.Timestamp,
}
// 新增：
out.MentionedBotUserIDs = make([]string, 0, len(m.Mentions))
for _, u := range m.Mentions {
    if u != nil {
        out.MentionedBotUserIDs = append(out.MentionedBotUserIDs, u.ID)
    }
}
out.MentionEveryone = m.MentionEveryone
```

`bot.Message` 新增两个字段：

```go
type Message struct {
    // ...existing...
    MentionedBotUserIDs []string
    MentionEveryone     bool
}
```

daemon 在写 `messages` 行时：

1. 按 `bot_user_id → accounts.id` 反查每个 mentioned user
2. 过滤出**该 room 当前成员**（被 mention 的非成员不算）
3. 写 `message_mentions(message_id, account_id)` 表
4. `MentionEveryone` 单独存 `messages.mention_everyone` 列（保留这一个 bool，跟 @everyone 一一对应；不是原 mention_all 的换名）

### 5.2 出站（agentchat → Discord）

`SendMessage` (`internal/bot/discord/discord.go:204`) 在调 Discord 前：

> **`roomMembers` 从哪来**：daemon API handler（`internal/api/v1/messages.go`
> 里 `SendMessage`）在调 `bot.Provider.SendMessage` 之前查
> `store.MembershipRepo.ListByRoom(roomID)` 拿到该 room 当前所有 account
> （含每个 account 的 `name` 和 `bot_user_id`），打包成 `roomMembers
> []bot.RoomMember`（新增类型）通过 `bot.SendOptions.RoomMembers` 字段传给
> provider。provider 自己不直接访问 store——保持 bot 层与业务层解耦。

```go
parsed := parseMentions(content, opts.RoomMembers)
// parsed.RewrittenContent — content with @<name> → <@bot_user_id>
// parsed.UserIDs           — bot user IDs to ping
// parsed.Everyone          — bool

repliedUser := opts.ReplyToMessageID != ""  // reply 时保留 Discord 默认 ping 原作者

send := &discordgo.MessageSend{
    Content: parsed.RewrittenContent,
    AllowedMentions: &discordgo.MessageAllowedMentions{
        Users:       parsed.UserIDs,
        Parse:       []discordgo.AllowedMentionType{},
        RepliedUser: repliedUser,
    },
}
if parsed.Everyone {
    send.AllowedMentions.Parse = append(send.AllowedMentions.Parse, discordgo.AllowedMentionTypeEveryone)
}
if opts.ReplyToMessageID != "" {
    send.Reference = &discordgo.MessageReference{
        MessageID: opts.ReplyToMessageID,
        ChannelID: channelID,
    }
}
```

**`AllowedMentions` 默认全禁 + 显式白名单**：防 content 里偶然出现 `@everyone`
字面量被 Discord 误触发；只有 daemon 解析确认要 ping 的才进 AllowedMentions。
`RepliedUser` 在 reply 路径上**显式置 true**——保留 Discord 客户端默认体验，
也避免被"默认全禁"逻辑误覆盖。

**架构调整**：当前代码在 fast path（无附件）走 `ChannelMessageSendReply` /
`ChannelMessageSend`，slow path（含附件）走 `ChannelMessageSendComplex`。
新设计需要在所有路径上注入 `AllowedMentions`，因此**统一改走 `MessageSend`
结构 + `ChannelMessageSendComplex`**——`ChannelMessageSendReply` 这个 helper
内部本来就是套壳调 Complex（`discordgo.restapi.go:1829-1837` 实际包装
`ChannelMessageSendComplex`）。统一路径让 attachment / reply / mention
三种维度的代码不再分支。

### 5.3 路径总结

```
[发送方 agent]
    │ agentchat send room-xxx "@alice 看下"
    ▼
[CLI] → POST /v1/rooms/{room}/messages { content: "@alice 看下", ... }
    │
    ▼
[daemon] 解析 @alice → 找到 alice 的 bot_user_id (假设 98765)
    │ Discord SendMessage:
    │   content = "<@98765> 看下"
    │   AllowedMentions.Users = [98765]
    │ SQLite:
    │   INSERT messages (content="<@98765> 看下", ...)
    │   INSERT message_mentions (msg_id, alice_account_id)
    ▼
[Discord]
    │ MESSAGE_CREATE 推给该 channel 的所有 bot（包括 alice 的 bot）
    │ alice 的 bot user 收到这条带 mention
    ▼
[daemon - 接收侧]
    │ alice 的 connector 收到 MESSAGE_CREATE
    │ handleMessageCreate → discordToMessage
    │ 看到 m.Mentions 里有自己 (bot_user_id=98765)
    │ 但这条**已经**在 daemon 的 SQLite 里（发送方写的）
    │ → 走幂等去重，不重复入库
    ▼
[state.Bus.Publish(alice)] → alice 的 watch state snapshot 重算
[alice 的 state]
    │ totals.unread += 1
    │ totals.mentions += 1  (因为 message_mentions 里有 alice)
    │ mentions[] 里加这条
```

### 5.4 边界与陷阱

| 情况 | 行为 |
|---|---|
| 发送方写 `@alice` 但 alice 不在该 room | 字面量原样保留；message_mentions 不写；alice 不会被 ping |
| alice 在 room 但 alice 此刻 offline（bot 没连）| alice 上线后从 SQLite 读到自己被 mention 仍然算 |
| alice 被 mention 后被 admin 踢出 room | alice 仍能在 history 里看到那条（如果有 retention），但 state.mentions 不再列（只取当前 room 成员的） |
| 两个 alice 同名 | 不可能——`UNIQUE(accounts.name)` |
| `@alice` 出现在代码块 / 引用块里 | 当前实现**仍然解析**（简单）；如果用户抱怨可以加 Markdown 感知，YAGNI 阶段不做 |

---

## 6. **核心约束：username = bot name 一致**

agent 写 `@alice` 能稳定解析的基石。**方案 C**：`set-discord` 时校验，不一致 CONFLICT。

### 6.1 校验流程

```bash
agentchat admin account create --name alice --role user
# accounts 表: { name: "alice", ... }

# admin 在 Discord Developer Portal 把 application/bot 改名为 "alice"，然后:
agentchat admin account set-discord <id> --bot-token "MTI..."

# daemon 在 set-discord 内:
#   1. 用 token 连上 GET /users/@me 拿 bot.Username
#   2. 比对 account.name == bot.Username
#   3. 不一致 → CONFLICT:
#        "bot username \"agent-alice-1234\" != account name \"alice\""
#        "fix: rename the bot on Discord Developer Portal to \"alice\", or pass --force-rename"
```

### 6.2 救命 flag

```bash
agentchat admin account set-discord <id> --bot-token "MTI..." --force-rename
# daemon 调 discordgo.Session.UserUpdate(account.name, "", "")
# 内部走 PATCH /users/@me（confirmed: discordgo restapi.go:362）
# 遇 Discord username rate limit (2/h) → UNAVAILABLE，提示 retry-after
```

### 6.3 `admin account rename` 命令

保留，但加约束：

```bash
agentchat admin account rename <id> --name bob
# 必须满足:
#   - 该账号 lifecycle_state in (created, offline)（不能 online 时改）
#   - 没绑 bot token（set-discord 之前可任意改）
#     或者
#     有 bot token 但传了 --force-rename：daemon 同步 PATCH /users/@me
# 否则 CONFLICT
```

### 6.4 `accounts.name` 已经是 UNIQUE

`migrations/0001_init.up.sql` 第 6 行（`accounts` 表）：

```sql
name TEXT NOT NULL UNIQUE,
```

不需要新加约束。本设计稿假定此前提，所有 `@<name>` 解析依赖它。

---

## 7. state schema 简化

```jsonc
{
  "version": ...,
  "account_id": ...,
  "emitted_at": "...",

  "totals": {
    "unread": 3,                  // 保留
    "mentions": 1,                // 语义改: "未读 ∩ @我"（含 @everyone）
    "priority": 0,                // 保留
    "announcements": 1,           // 保留
    "system_announcements": 0     // 保留
    // ❌ "pending_acks" 删除
  },

  "rooms":            [ /* per-room unread */ ],
  "mentions":         [ /* @我的未读，最多 50 */ ],
  // ❌ "pending_acks": [...]
  "priority":         [ /* urgent + system 未读，最多 50 */ ],
  "new_rooms":        [ /* 24h 内新加，最多 5 */ ],
  "recently_active":  [ /* 订阅房间按最后消息时间，最多 20 */ ],
  "announcements":    [ /* 未读群公告，最多 20 */ ],
  "system_announcements": [ /* 未读系统公告，最多 20 */ ],
  "health": {...}
}
```

`mentions` 维度的查询逻辑（**简化示意**——真实
`store.MessageStateRepo.CountMentionsForSubscribed`（`internal/store/sqlite/message_state_repo.go:89-103`）
还含订阅过滤、archived 过滤、author!=self 等 WHERE 项；这里只列关键 mention 谓词的变化）：

```sql
-- 老（M6 实现）
WHERE m.mention_all = 1 AND ms.read_at IS NULL

-- 新（M9）
WHERE ms.read_at IS NULL
  AND (
       m.mention_everyone = 1
       OR EXISTS (
           SELECT 1 FROM message_mentions mm
            WHERE mm.message_id = m.id AND mm.account_id = ?
       )
     )
```

---

## 8. 数据库改动

### 8.1 新表

```sql
CREATE TABLE message_mentions (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, account_id)
);
CREATE INDEX idx_message_mentions_account ON message_mentions(account_id);
```

### 8.2 列改动

| 表 | 列 | 操作 |
|---|---|---|
| `messages` | `requires_ack` | **DROP** |
| `messages` | `mention_all` | **DROP**（被 message_mentions + mention_everyone 替代） |
| `messages` | `mention_everyone INTEGER NOT NULL DEFAULT 0 CHECK (mention_everyone IN (0,1))` | **ADD** |
| `message_states` | `replied_at` | **DROP** |

`accounts.name` 已经是 `UNIQUE`，不动。

### 8.3 Migration 顺序

`modernc/sqlite v1.50.1` 内嵌 SQLite **3.53.1**（go pkg cache 里
`lib/sqlite_*.go` 的 `SQLITE_VERSION`），> 3.35 → **`ALTER TABLE DROP COLUMN`
直接可用**，不需要重建表。

注：现有迁移已用到 `0004_m7_attachments.up.sql`，下一档为 `0005_*`。

```sql
-- 0005_m9_mentions.up.sql
CREATE TABLE message_mentions (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, account_id)
);
CREATE INDEX idx_message_mentions_account ON message_mentions(account_id);

ALTER TABLE messages ADD COLUMN mention_everyone INTEGER NOT NULL DEFAULT 0
    CHECK (mention_everyone IN (0,1));

-- 老的 mention_all=1 数据迁到 mention_everyone（行为最接近）
UPDATE messages SET mention_everyone = mention_all;

ALTER TABLE messages       DROP COLUMN requires_ack;
ALTER TABLE messages       DROP COLUMN mention_all;
ALTER TABLE message_states DROP COLUMN replied_at;
```

### 8.4 写入路径

`messages.content` 列存的是 **daemon 重写后**的 Discord 原生格式（含
`<@bot_user_id>`），不是用户原始输入的 `@<name>` 字面量。这保证：

- 历史 history 渲染 `display_content` 时按 `<@id>` → `@<name>` 反查唯一
- 数据库里所有消息的引用关系都用 stable 的 `bot_user_id`，不会因为账号改名漂移

---

## 9. API 端点变化

### 9.1 新增

| 方法 | 路径 | 含义 |
|---|---|---|
| `POST` | `/v1/rooms/{id}/read` | 实现 §3，body `{"limit": 10, "before": null}` |

### 9.2 删除

| 方法 | 路径 |
|---|---|
| `GET` | `/v1/rooms/{id}/messages` |
| `POST` | `/v1/messages/{id}/read` |
| `POST` | `/v1/messages/{id}/reply-ack` |

### 9.3 修改

| 端点 | 改动 |
|---|---|
| `POST /v1/rooms/{id}/messages` (send) | 入口接收的 body 删除 `requires_ack` / `mention_all` 字段；daemon 内部多一步 parse mention 注入 AllowedMentions |
| `POST /v1/accounts/{id}/discord` (set-discord) | 加 bot username 校验；新增 `?force_rename=true` query |

### 9.4 内部 handler

- 新 `ReadRoom` handler 内部按 `before` 是否存在 dispatch 到两条路径
- `MarkRead` / `ReplyAck` handler 直接删
- `SendMessage` handler 在调 `bot.Provider.SendMessage` 前多一步 `parseMentions`

---

## 10. CLI 命令变化总览

```bash
# === 保留 ===
agentchat whoami
agentchat state / watch state
agentchat room list / show / members / subscribe / unsubscribe / announce / announce-show
agentchat ack-announcement <ann-id>            # 群公告 ack 仍然独立
agentchat system-announcements / ack-system    # 系统公告独立
agentchat admin ...                            # 所有 admin 命令不变

# === 改 ===
agentchat send <room> [text] [--reply <msg>] [--priority …] [--attach …]
                                               # 删 --all / --requires-ack；@ 写在正文
agentchat read <room> [--limit N] [--before <msg>]
                                               # 新 verb（取代 history / read <msg>）

# === 删 ===
agentchat history <room>                       # → read <room> --before
agentchat read <msg-id>                        # → read <room>
agentchat reply-ack <msg-id>                   # 没了，被 @ 系统替代
```

---

## 11. 实施 phase 划分（M9）

### Phase 1（schema + Discord 入站）

1. SQLite migration `0005_m9_mentions.up.sql`（§8.3）
2. `bot.Message` 加 `MentionedBotUserIDs` / `MentionEveryone` 字段
3. `discordToMessage` 读 Discord mention 字段
4. daemon ingester 写 `message_mentions` + `mention_everyone`
5. `store.MessageStateRepo.CountMentionsForSubscribed`（在 `internal/store/sqlite/message_state_repo.go`，由 `internal/state/aggregator.go` 调用）改新查询逻辑（§7）
6. 测试：单元 + 集成（fake Discord provider 触发 mention 事件）

### Phase 2（出站 + CLI + 命令收敛）

7. `parseMentions(content, roomMembers)` 实现
8. `SendMessage` 注入 `AllowedMentions` + content 重写；统一所有路径走
   `ChannelMessageSendComplex`（§5.2）
9. 新 `POST /v1/rooms/{id}/read` handler
10. CLI 加 `read <room>` 命令
11. `set-discord` 加 bot username 校验 + `--force-rename`（用
    `discordgo.Session.UserUpdate`）
12. 删除：CLI `history` / `read <msg>` / `reply-ack`；API `GET /rooms/.../messages` / `POST /messages/.../read` / `POST /messages/.../reply-ack`；`send` 的 `--all` / `--requires-ack` flag
13. 删除：state `pending_acks` 维度

#### Phase 2 待改 Go 代码清单

`requires_ack` / `mention_all` / `replied_at` 在代码里有 20+ 处引用，
按文件分组：

| 文件 | 改动 |
|---|---|
| `internal/store/types.go` | `Message.RequiresAck` / `Message.MentionAll` 字段 DROP；`MessageState.RepliedAt` 字段 DROP |
| `internal/store/store.go` | `MessageRepo.Create` 注释 / interface 签名同步；删 `CountPendingAcksForSubscribed` + `ListPendingAcksForSubscribed` 接口；`Message` 结构字段同步 |
| `internal/store/sqlite/message_repo.go` | INSERT / UPSERT / Scan / Update 全部 SQL 删 `requires_ack` / `mention_all` 列；Go 字段同步删 |
| `internal/store/sqlite/message_state_repo.go` | Upsert SQL 删 `replied_at`；Scan 删；删 `CountPendingAcksForSubscribed` + `ListPendingAcksForSubscribed` 实现；`CountMentionsForSubscribed` 改新查询（§7） |
| `internal/api/v1/messages.go` | 删 `MarkRead` / `ReplyAck` / `ListMessages` handler；删 `mutateMessageState` helper（read 路径合并进 `ReadRoom`）|
| `internal/api/v1/helpers.go` | `MessageToResponse` 删 `MentionAll` / `RequiresAck` 字段；`MessageStateToResponse` 删 `RepliedAt` 字段 |
| `internal/api/v1/types.go` | `MessageResponse` / `MessageStateResponse` 字段同步删；`SendMessageRequest` 删 `RequiresAck` / `MentionAll` 字段；删 `MessageListResponse`（对应 `GET /v1/rooms/{id}/messages` 端点被删）；新增 `RoomReadRequest` / `RoomReadResponse` |
| `internal/api/server.go` | 删 `/messages/{id}/read` / `/messages/{id}/reply-ack` / `GET /rooms/{id}/messages` 路由；加 `/rooms/{id}/read` 路由 |
| `internal/state/types.go` | `Snapshot.PendingAcks` 字段 DROP；`Totals.PendingAcks` 字段 DROP |
| `internal/state/` 聚合器 | 删 PendingAcks 维度计算 |
| `internal/bot/discord/discord.go` | `discordToMessage` 加 mention 解析；`SendMessage` 统一 Complex 路径 + AllowedMentions（§5.1 / §5.2） |
| `internal/bot/types.go` | `Message` 加 `MentionedBotUserIDs` / `MentionEveryone`；新增类型 `RoomMember{AccountID, Name, BotUserID}`；`SendOptions` 加 `RoomMembers []RoomMember`（API handler 注入；见 §5.2） |
| `pkg/client/client.go` | 删 `MarkMessageRead` / `ReplyAckMessage` / `ListMessages`（对应 `GET /v1/rooms/{id}/messages` 端点也被删）；`SendMessageOptions` 删 `RequiresAck` / `MentionAll` 字段；加 `ReadRoom` |
| `cmd/agentchat/cmds/message.go` | 整个文件**删除**（`read` / `reply-ack` 都没了） |
| `cmd/agentchat/cmds/history.go` | 整个文件**删除** |
| `cmd/agentchat/cmds/send.go` | 删 `--all` / `--requires-ack` flag |
| `cmd/agentchat/cmds/read.go` | **新增**（实现 `read <room>` 命令） |
| `cmd/agentchat/cmds/admin_account.go` | `set-discord` 加 `--force-rename` flag |
| `internal/api/*_test.go`、`pkg/client/m4_test.go`、`internal/store/sqlite/sqlite_m{4,5}_test.go`、`internal/state/aggregator_test.go` | 受影响的测试要全部更新（M4 / M5 / M6 路径多处） |

### Phase 3（文档 + audit）

14. `USAGE.md` / `USAGE-USER.md` 全面重写
15. user-driven 3-agent audit（按 `docs/05-engineering-workflow.md`）
16. 修 finding，user 签字关闭 M9

---

## 12. 文档需要同步

| 文件 | 改动 |
|---|---|
| `docs/02-requirements-final.md` | §3.3 删 requires_ack / replied / reply 关系；§3.5 公告表里 @all 那行改成 "Discord 原生 @everyone"；§5.2.1 状态界面**删除第 4 条**（"要求我回复但未回复的消息列表"），其它 6 条 + 健康栏保留；同时补 M6 加的 announcements / system_announcements 两个独立维度（当前需求文档还停在 M5 时期的 7+1） |
| `docs/USAGE.md` | §7 主循环、§8 命令清单、§9 state schema、§10 错误码全部改 |
| `docs/USAGE-USER.md` | §2 主循环示例改两步：`watch state → read → send`；§3 删 reply-ack；§5 删群发；§6 schema 改 |
| `docs/00-overview.md` | M9 进度条 |
| `docs/04-roadmap.md` | 新增 M9 条目 |

---

## 13. Open questions（已敲定）

| # | 问题 | 结论 |
|---|---|---|
| Q1 | `accounts.name` UNIQUE 状态 | 已存在（`0001_init.up.sql`），无需新增 |
| Q2 | username = bot name 实现方案 | **C（校验拒绝）+ `--force-rename` 救命 flag** |
| Q3 | content 里 `@<name>` 字面量自动解析 | ✅ 做（取代 `--mention` flag） |
| Q4 | `requires_ack` / `replied_at` / `mention_all` 列 | ✅ 直接 DROP（v0.0.x 阶段） |
| Q5 | 历史 reply_ack 数据丢失 | ✅ 可接受 |
| Q6 | `--all` flag | ✅ 删除，用正文 `@everyone` |
| Q7 | `--mention` flag | ✅ 不需要——@ 写在正文 |
| Q8 | 公告归到 unread | ❌ 独立 `announcements[]` 维度（per-version read state） |
| Q9 | `@here` / `@<role>` 支持 | ❌ 只支持 `@<name>` 和 `@everyone` |

---

## 14. 验收（M9 完成后这套 agent 主循环能跑通）

```bash
export AGENTCHAT_TOKEN=agch_...

agentchat watch state --json | while IFS= read -r frame; do
    rooms_with_unread=$(echo "$frame" | jq -r '.rooms[] | select(.unread > 0) | .room_id')
    for room in $rooms_with_unread; do
        # 一条命令：看 + 标已读 + 拿上下文
        ctx=$(agentchat read "$room" --json)
        # 决策（agent 模型推理）
        decision=$(echo "$ctx" | my-llm-call)
        # 一条命令：回复（Discord 原生 reply + 正文 @ 解析）
        echo "$decision" | jq -r '.target_msg, .reply' | {
            read target
            read reply
            agentchat send "$room" --reply "$target" "$reply"
        }
    done
done
```

**两步循环 + 两个 verb**，无 `history`、无 `read <msg>`、无 `reply-ack`、
无 `--mention`、无 `--all`。Discord 客户端里的人类用户用 `@alice` 真的能 ping
到 alice 的 agent，alice 的 agent 用 `send "@bob ..."` 真的能 ping 到 bob。

---

## 15. 不做

- **不**保留旧命令 / 旧列的兼容层
- **不**支持 `@here` / `@<role-id>`（YAGNI；agent 场景用不到）
- **不**改 announcement 系统（@ 系统和公告系统正交）
- **不**做"已读撤销"（COALESCE 不允许）
- **不**做 content Markdown 感知（代码块里的 @<name> 也解析；用户抱怨再说）
