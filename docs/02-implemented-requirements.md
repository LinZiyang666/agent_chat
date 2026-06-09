# 02 · 已实现的需求清单

> 用 **"代码里真的能跑"** 的视角列出当前实现满足的需求。每一项都附 CLI / API / 代码索引，便于核对。未实现的内容放在文末"刻意不做"小节。
>
> 适配代码版本：M9 Phase 2 之后。

---

## 1. 进程与部署

| # | 需求 | 实现 |
|---|---|---|
| 1.1 | 一个进程 `agentchatd` 守护，一个进程 `agentchat` CLI | [`cmd/agentchatd`](../cmd/agentchatd), [`cmd/agentchat`](../cmd/agentchat) |
| 1.2 | 单 data-root 排他独占，第二份立刻拒绝 | flock on `<data-root>/agentchatd.lock`（[`datalock.go`](../cmd/agentchatd/cmds/datalock.go)）→ `CONFLICT` |
| 1.3 | CLI ↔ daemon 走本机 UDS，外网不暴露 | `net.Listen("unix", cfg.SocketPath)` + `chmod 0o600`（[`serve.go:160-170`](../cmd/agentchatd/cmds/serve.go)） |
| 1.4 | data-root / master.key 权限收紧 | `EnsureDataRoot` 强制 0o700（[`config.go:230-238`](../internal/config/config.go)）；`LoadOrCreateMasterKey` 每次启动 chmod 0o600 |
| 1.5 | 配置 TOML + env 双轨，env 覆盖 TOML | [`config.go:Load`](../internal/config/config.go) |
| 1.6 | graceful shutdown：SIGINT/SIGTERM → 关 HTTP + 断 Discord + reconcile lifecycle | [`serve.go:198-228`](../cmd/agentchatd/cmds/serve.go) |
| 1.7 | daemon 启动 / 关闭时 reconcile "stale online" 账号 | `reconcileStaleOnlineLifecycles`（boot + shutdown 都跑） |
| 1.8 | 数据库 schema migrations 自动应用 | embedded `migrations/*.up.sql` + `schema_migrations` 表（[`db.go:migrate`](../internal/store/sqlite/db.go)） |
| 1.9 | 首次启动自动创建 root admin + 打印一次 API token | `bootstrapRoot`（[`serve.go:291-320`](../cmd/agentchatd/cmds/serve.go)） |
| 1.10 | 多 bot / 多账号常驻，每 bot 独立 WS | `connector.Connector.instances[accountID]` |

## 2. 账号与权限

| # | 需求 | 实现 |
|---|---|---|
| 2.1 | 两个 role：admin / user | `store.Role` 枚举 + `auth.RequireAdmin` 中间件 |
| 2.2 | admin 可：CRUD 账号、改 role、改 name | `POST/GET/PATCH/DELETE /v1/accounts*` |
| 2.3 | name 唯一，重名失败有明确 `CONFLICT` | `accounts.name UNIQUE` |
| 2.4 | 删账号级联清理 token | `tokens.account_id FK ON DELETE CASCADE` |
| 2.5 | 删账号时若仍有 live Provider → 拒绝 | `DeleteAccount` handler 调 `Connector.IsRegistered` |
| 2.6 | account.name 与 Discord username 同步：Discord 是权威 | `SetDiscord` 调 `IdentityProber.Probe` → 写回 `accounts.name`；`UpdateAccount` 改 name 时先调 `Prober.Rename`（限流→ `UNAVAILABLE`） |
| 2.7 | 改 name 撞别人 → `CONFLICT`（不允许偷偷顶号） | `accounts.name UNIQUE` |
| 2.8 | lifecycle 状态：created / online / offline / archived / deleted（用 4 个） | `LifecycleState` 枚举 |

## 3. API token

| # | 需求 | 实现 |
|---|---|---|
| 3.1 | token 形如 `agch_<id>_<secret>`；id 公开，secret 不公开 | [`auth/token.go`](../internal/auth/token.go) |
| 3.2 | token_hash 用 bcrypt 存（cost 12） | `crypto.HashAPIToken` / `VerifyAPIToken` |
| 3.3 | bcrypt 在事务外做（不阻塞 SQLite 单写） | `Manager.PrepareToken` → `PersistTokenVia` 分拆 |
| 3.4 | 创建后返回 raw 一次，再也拿不到 | `CreateTokenResponse.Raw`；`ListTokens` 不返回 hash / secret |
| 3.5 | 可吊销 / 可查 last_used_at | `RevokeToken` + `TouchLastUsed`（best-effort） |
| 3.6 | 撤销 / 过期 → `AUTH_REVOKED` | `auth.Manager.Verify` |
| 3.7 | bearer-only；header 缺失 / 格式错 → 区分 `AUTH_MISSING` / `AUTH_INVALID` | `auth.Middleware.Handler` |
| 3.8 | CLI 解析 token 优先级：`--token` > `AGENTCHAT_TOKEN` env > `<data-root>/cli.toml` | [`root.go:resolveToken`](../cmd/agentchat/cmds/root.go) |

## 4. Discord 接入

| # | 需求 | 实现 |
|---|---|---|
| 4.1 | 一个账号 ↔ 一个 Discord application + bot token | `accounts.bot_token_enc` (AES-256-GCM, master.key 加密) |
| 4.2 | bot token 加密存盘，明文不落盘 | `crypto.AESGCMEncrypt` in `SetDiscord` handler |
| 4.3 | `set-discord` 必须先 verify token 才能保存 | `IdentityProber.Probe` 在 SetDiscord 里跑（M9-P2） |
| 4.4 | `set-discord` 自动同步 username + 写 `bot_user_id` | Probe 返回 → 写回 + audit `account.set_discord` |
| 4.5 | `online` 真正开 Discord gateway，收到 `Ready` 才返回成功 | `discord.Provider.Connect` 阻塞等 `ready` chan，30 s 超时 |
| 4.6 | `online` 后注册 ingester goroutine，开始持久化收消息 | `OnlineAccount` 调 `Ingester.AttachAccount` |
| 4.7 | `offline` 先提交 lifecycle 改动 + audit，再断连 | M3-P3-002 ordering：`OfflineAccount` 先 tx，再 `Disconnect` |
| 4.8 | 单 guild，guild_id 来自 config | `config.DiscordConfig.GuildID`；未配置时 `CreateChannel` → `InvalidArgument` |
| 4.9 | privileged intents 显式要求 | `IntentsGuilds + GuildMembers + GuildMessages + MessageContent`（[`discord.go:94-99`](../internal/bot/discord/discord.go)） |
| 4.10 | Discord 端被删 channel → 本地 room 自动 archive | `EventChannelDeleted` → `Ingester.ingestChannelDeleted` |

## 5. Room 与成员

| # | 需求 | 实现 |
|---|---|---|
| 5.1 | room = 一个 guild text channel | `rooms.discord_channel_id UNIQUE` |
| 5.2 | admin 可建 / 改名 / 归档 / 删除 room | `CreateRoom / UpdateRoom / ArchiveRoom / DeleteRoom` |
| 5.3 | 删 room 级联清理 memberships + messages + message_states | FK `ON DELETE CASCADE` |
| 5.4 | 删 room 时 Discord channel 也删 | `DeleteRoom` 在 tx 外调 `Provider.DeleteChannel`（M4-P3-001 ordering） |
| 5.5 | 已 archived 的 room 不能 send / announce | handler 显式 check `room.Archived` |
| 5.6 | admin 可 invite/kick 成员；user 不可 | `RequireAdmin` 子组 |
| 5.7 | 成员加入需先有 `bot_user_id`（要做 Discord per-channel override） | `InviteMember` 在 read tx 内校验 `target.BotUserID != ""` |
| 5.8 | 成员有"已订阅 / 旁观（未订阅）"两态，自助切 | `PATCH /v1/memberships/{room_id}` + `Membership.Subscribed` |
| 5.9 | 旁观成员仍然收消息行（写 message_state），只是不进 primary state | `Ingester` / `SendMessage` 给所有 member 写 `message_states`；state 聚合按 `subscribed` 过滤 |
| 5.10 | 列 room / show room / 列 members | `GET /v1/rooms[/{id}/members]`；非 admin user 只看到自己所属 room |
| 5.11 | room 排他索引：admin 看全部，user 看自己 member 的 | `RoomRepo.List` / `ListByMember` |

## 6. 消息收发（M9 Phase 2 两动词）

| # | 需求 | 实现 |
|---|---|---|
| 6.1 | 一个 verb `send` 发消息（不含 ack / reply-ack / requires-ack 等） | [`send.go`](../cmd/agentchat/cmds/send.go) → `POST /v1/rooms/{id}/messages` |
| 6.2 | 一个 verb `read` 看消息（默认：未读 + 上下文 + 同事务标读） | [`read.go`](../cmd/agentchat/cmds/read.go) → `POST /v1/rooms/{id}/read` |
| 6.3 | `read --before` 历史分页（不动 read state） | `ReadRoomRequest.Before` |
| 6.4 | 消息优先级 normal / urgent / system；system 仅 admin 可发 | `MessagePriority.Valid` + handler 显式拒 user `priority=system` |
| 6.5 | 回复某条消息（Discord native reply） | `--reply <msg-id>` → `bot.SendOptions.ReplyToMessageID` |
| 6.6 | 正文支持 `@<name>` / `@everyone` mention | `bot.ParseMentions` 重写 → `<@bot_user_id>`，AllowedMentions 默认拒 |
| 6.7 | 拒绝正文里手写 raw `<@123>` | `rawMentionRe` 检测 → `InvalidArgument` |
| 6.8 | mention 解析必须基于 room 当前成员快照（防跨 room 误 ping） | send handler 在同 tx 拿 memberships + accounts，传给 ParseMentions |
| 6.9 | send + ingest gateway echo race-safe | `messages.discord_msg_id UNIQUE` + `CreateIgnoreConflict / ApplySendMetadata / MergeMentionEveryone / AddForMessage` |
| 6.10 | 每条消息为每个成员（含旁观）写一行 message_state，author 标 read | `SendMessage` 内的 fan-out 逻辑（M4-P3-005 fix） |
| 6.11 | 附件上传（M7），单文件 ≤ 10 MB | `--attach` flag；`DiscordAttachmentLimit = 10 MiB` |
| 6.12 | 附件路径校验：不跟 symlink，不要 dir，不允许非 regular | `os.Lstat` + checks（M8-S-P2-004） |
| 6.13 | 消息体 cap：HTTP body ≤ 1 MiB | `DecodeJSON` 用 `http.MaxBytesReader`；超 → `PayloadTooLarge` |

## 7. 收件箱：State 聚合

> 详见 [`internal/state`](../internal/state)。Snapshot 共 8 + 公告 2 = 10 维。

| # | 需求 | 实现 |
|---|---|---|
| 7.1 | Totals：unread / mentions / priority / announcements / system_announcements | `CountUnreadForSubscribed` / `CountMentionsForSubscribed` / `CountPriorityForSubscribed` + announcement repo 各自 Count |
| 7.2 | 每房间未读（只算订阅 + 非 archived） | `UnreadCountByRoomForSubscribed` |
| 7.3 | Mentions 流：被 @ 的未读消息（含 @everyone） | `ListMentionsForSubscribed` |
| 7.4 | Priority 流：urgent + system 未读 | `ListPriorityForSubscribed` |
| 7.5 | New rooms：24 h 内新加入的订阅房间 cap 5 | `buildRoomFeeds` |
| 7.6 | Recently active：订阅房间按最后消息时间 cap 20 | `LatestPerRoomForMember` + sort |
| 7.7 | 未读群公告 / 系统公告 list cap 20 | `ListUnreadForAccount` |
| 7.8 | Health bar：token_ok / provider_status / discord_reachable | `Aggregator.Build` 调 `ProviderStatusFn` |
| 7.9 | `agentchat state` 一次性快照 | `GET /v1/state` → `BuildNow` |
| 7.10 | `agentchat watch state` NDJSON 长连接，事件驱动 | `GET /v1/state/watch` + `state.Bus.Subscribe` |
| 7.11 | 同一账号最多 8 个 watch 订阅 | `MaxSubscribersPerAccount` → `RESOURCE_EXHAUSTED` |
| 7.12 | 200 ms 防抖：突发 mutation 合并成一帧 | `Bus.Publish` + `time.AfterFunc(debounce)` |
| 7.13 | Snapshot 单调 `version` 字段（漏帧可检测） | `Bus.version atomic.Int64` |
| 7.14 | 慢消费者不会阻塞 daemon | `fire` 用 non-blocking send（select default） |
| 7.15 | Totals 是真实总数；list 维度可截 | counts 与 lists 走不同 repo 调用（M5-P3-002 fix） |

## 8. 公告（M6）

| # | 需求 | 实现 |
|---|---|---|
| 8.1 | 群公告：发即版本+1，所有成员变未读，必读 | `Announcements.Create` 内分配 `version = NextVersion(room) + 1`；fan-out → publish all members |
| 8.2 | 任成员可发群公告（admin 旁路 membership） | `CreateAnnouncement` handler |
| 8.3 | 看公告（最新版本 + 自己是否 ack） | `GET /v1/rooms/{id}/announcement` |
| 8.4 | ACK 公告（幂等覆写 read_at） | `MarkAnnouncementRead` → `AnnouncementReads.Upsert` |
| 8.5 | 群公告同时镜像到 Discord channel（best-effort） | `mirrorAnnouncementBestEffort`；失败仅 log，不回滚 |
| 8.6 | 系统公告：admin only，全员未读 | `CreateSystemAnnouncement` 写完 publish 所有账号 |
| 8.7 | 列 / ACK 系统公告 | `GET /v1/system/announcements` + `POST /v1/system/announcements/{id}/read` |

## 9. 附件（M7）

| # | 需求 | 实现 |
|---|---|---|
| 9.1 | inbound：Discord MESSAGE_CREATE 带附件 → 落 `attachments` 行（local_path 空） | `Ingester.ingestNew` 调 `Attachments.Create` |
| 9.2 | 后台 downloader 拉文件到本地 cache | `internal/attachment/downloader.go` |
| 9.3 | cache 布局 `<data-root>/attachments/<msg-id>/<att-id>/<safe-filename>` | `fetchOne` |
| 9.4 | 下载失败 → 指数退避（2/4/8/.../120 s） | `backoffFor` |
| 9.5 | fsync 后 rename，崩溃后没有半文件 | `tmp.Sync() + os.Rename` |
| 9.6 | 校验 sha256 + Content-Length 一致 | `MarkDownloaded` 写 sha256；gateway 撒谎 → `Unavailable` |
| 9.7 | outbound：send `--attach` 上传，daemon 直接读本地路径 | `bot.UploadFile` |
| 9.8 | outbound + ingest echo 不会双写附件行 | send 路径检测 `existing` → `MarkDownloaded` patch（M9 audit fix） |
| 9.9 | 文件名清洗：拒 `..`、控制字符、长度截 80 | `safeFilename` |
| 9.10 | 文件大小 cap：单文件 ≤ 10 MB outbound（Discord 限制），cache 单文件 ≤ 50 MiB inbound 默认 | `DiscordAttachmentLimit` / `Downloader.maxBytes` |

## 10. Audit log

| # | 需求 | 实现 |
|---|---|---|
| 10.1 | 所有管理动作有 audit 行 | `audit.Recorder.RecordVia` 与 mutation 同 tx |
| 10.2 | 覆盖动作清单 | `account.create / update / delete / set_discord / online / offline / lifecycle_reconcile`，`token.create / revoke`，`room.create / rename / archive / delete / member.add / member.remove`，`membership.subscribe / unsubscribe`，`message.send / read`，`announcement.create / read`，`system_announcement.create / read`，`debug.send` |
| 10.3 | 可按 account / since / limit 过滤 | `GET /v1/audit?account=...&since=...&limit=...` |
| 10.4 | reconcile 用 actor `"system"` 标记 | `reconcileStaleOnlineLifecycles` payload 含 old_state + reason |
| 10.5 | debug.send 也入 audit（不让 diag 通道绕审计） | `DebugSend` handler 调 `Recorder.Record`（M8-S-P2-013） |

## 11. 错误处理与 CLI 体验

| # | 需求 | 实现 |
|---|---|---|
| 11.1 | 所有用户可见错误带 `errcode.Code` 字符串 | [`errcode/errcode.go`](../internal/errcode/errcode.go) |
| 11.2 | CLI exit code 稳定（10/11/12/13/20/21/22/50/51） | `errcode.ExitCode` |
| 11.3 | HTTP status 跟 Code 一一对应 | `errcode.HTTPStatus` |
| 11.4 | TTY 输出表格 / 人类视图，pipe 自动 JSON | `outputJSON()` |
| 11.5 | `--json` 强制 JSON | `flagJSON` |
| 11.6 | 错误 stderr 渲染：`Error [CODE]: msg\nCaused by: ...` | `cliutil.PrintError` |
| 11.7 | `--json` 时 stderr 是 `{"error":{"code","message","details"}}` | `cliutil.PrintErrorJSON` |
| 11.8 | panic 不会让 daemon 崩 | `middleware.Recover` |
| 11.9 | 每请求一行 access log | `middleware.Logger` |

## 12. 安全 / 加固

| # | 需求 | 实现 |
|---|---|---|
| 12.1 | 单 daemon 排他锁（防 split-brain） | flock + chmod data-root |
| 12.2 | bcrypt cost 12，可在测试里降级到 MinCost | `var APITokenCost = 12`（test override） |
| 12.3 | master.key 32B 随机 + 启动时强制 chmod 0o600 | `LoadOrCreateMasterKey` |
| 12.4 | HTTP body cap 1 MiB → `PAYLOAD_TOO_LARGE` | `MaxRequestBodyBytes` |
| 12.5 | request 用 `DisallowUnknownFields` 防字段 typo | `DecodeJSON` |
| 12.6 | 附件路径不跟 symlink、文件名清洗 | `SendMessage` + `safeFilename` |
| 12.7 | watch state subscriber 单账号 cap 8 | `MaxSubscribersPerAccount` |
| 12.8 | 路由错误时不泄露内部细节 | `Recover` 中间件统一返回 `INTERNAL`，详细信息只进 daemon log |
| 12.9 | UDS socket 0600，data-root 0700 | serve.go chmod，config.EnsureDataRoot |

## 13. 开发体验

| # | 需求 | 实现 |
|---|---|---|
| 13.1 | `make build` 一把出两个 binary | [`Makefile`](../Makefile) |
| 13.2 | `make test` / `make test-race` / `make cover` | Makefile targets |
| 13.3 | `make smoke` 跑 M1–M7 端到端冒烟 | [`e2e/m*-smoke.sh`](../e2e/) |
| 13.4 | 版本号 ldflags 注入 | `VERSION := git describe` |
| 13.5 | 离线 install 脚本 | [`scripts/install.sh`](../scripts/install.sh) |
| 13.6 | Discord-less 测试：`bot/mock` Provider + Prober | `internal/bot/mock` |
| 13.7 | Skill 文件（Claude Code 集成） | [`skills/agentchat-user/SKILL.md`](../skills/agentchat-user/SKILL.md), [`skills/agentchat-admin/SKILL.md`](../skills/agentchat-admin/SKILL.md) |

---

## 14. 刻意不做（Non-goals）

| 题 | 状态 | 备注 |
|---|---|---|
| 多 Discord guild 并存 | 不支持 | 一个 daemon = 一个 guild |
| Matrix / Slack / 其他 backend | 不支持，但接口预留 | `bot.Provider` 是平台无关接口；目前只有 `discord` + `mock` 两个实现 |
| `?since=<version>` 增量回放 | 拒绝（明确返回 `InvalidArgument`） | M5-P3-005 决定：bus 无消息历史，告诉客户端别误以为有 replay |
| Web UI / TUI / 浏览器端 | 不会做 | 设计上只有 CLI |
| Slash command / bot 命令面板 | 不会做 | 走 CLI，不在 Discord 端做交互 |
| 全文搜索 / 长期归档查询 | 不在 scope | SQLite 直接查就行；规模再大要的时候再说 |
| 多用户共享同 token | 反对 | 每 agent 一份 token；rotate 走 create + revoke |
| Web 暴露 HTTP | 不会做 | UDS only，跨主机请自己写 SSH tunnel |
| 内置 LLM 调度 | 不会做 | agentchat 是消息层，不调 LLM |
| 复杂 ACL / 角色细分 | 不会做 | 只有 admin / user 两档；要更细就在外层 wrapper |
| Discord 的 reaction / thread / voice | 不实现 | `reactions` 表是占位；thread / voice 出 scope |

---

## 15. 与旧需求文档的差异速查

> 旧 [`archive/01-requirements.md`](./archive/01-requirements.md) / [`archive/02-requirements-final.md`](./archive/02-requirements-final.md) 列了一些后来又改 / 删的需求。差异：

- 旧需求里"requires-ack" / "reply-ack" 的强制回执机制 → **M9-P2 删除**。"需要处理"信号由 `@-mention` 表达：被 @ 看了（read 即标读）就算处理。
- 旧需求里 `mention_all` 是消息层 boolean → **M9 拆成 `mention_everyone`（消息列）+ `message_mentions` 表（per-account）**。
- 旧需求里 `history <room>` / `read <msg>` 是独立 verb → **M9-P2 collapse 成 `read <room>` 一动词**。
- 旧需求假设 `set-discord` 只是存 token → **现在必须先用 Prober 校验、并强制同步 username**。
- 旧需求里没"stale online reconcile" → **新增 daemon boot/shutdown 都跑**。
- 旧需求里没"out-of-band Discord sync" → **现在 Discord 客户端手删 channel 会自动 archive 本地 room**。
