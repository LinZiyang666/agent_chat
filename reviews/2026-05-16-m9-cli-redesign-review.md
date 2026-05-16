# M9 CLI/Discord Mention Redesign Review

日期：2026-05-16

范围：审查暂存区及暂存区外的 M9 Phase 1 相关修改，并对照 `docs/06-cli-redesign.md`。

## 结论

审查结论：**通过**。

复审时已确认前次 2 个 P2 级实现风险已有针对性修复和测试覆盖：

- Discord gateway echo conflict path 现在会合并 `mention_everyone` 和 `message_mentions`，不会丢弃真实 Discord mention 元数据。
- 入站 per-user mention 现在按当前 room member set 过滤，符合蓝图中“被 mention 的非成员不算”的要求。

`go test ./... -count=1` 已通过。未发现新的阻断问题。

## Findings

### Resolved: 已存在消息的 Discord echo 会丢弃 M9 mention 元数据

位置：

- `internal/message/ingester.go:194-201`
- `internal/api/v1/messages.go:259-315`
- `internal/store/sqlite/message_repo.go:209-225`

初审问题：`ingestNew` 在 `CreateIgnoreConflict` 返回 `inserted=false` 时直接 `return nil`，因此不会合并 Discord gateway echo 上的 `MentionEveryone` / `MentionedBotUserIDs`。如果发送路径已先写入同一个 `discord_msg_id`，echo 上携带的真实 Discord mention 元数据会被跳过。

复审结论：已修复。`internal/message/ingester.go` 现在在 inserted 和 conflict 两条路径都会持久化 mention 元数据；`internal/store/sqlite/message_repo.go` 新增 `MergeMentionEveryone`，且 `ApplySendMetadata` 对 `mention_everyone` 使用 OR-merge 语义，避免后写的 false 覆盖已经观测到的 true。

覆盖测试：

- `internal/message/ingest_test.go::TestIngestConflictPathMergesMentionMetadata`
- `internal/message/ingest_test.go::TestApplySendMetadataDoesNotClobberMentionEveryone`
- `internal/store/sqlite/message_repo_test.go::TestMergeMentionEveryoneOrSemantics`

### Resolved: 入站 per-user mention 未按当前 room 成员过滤

位置：

- `docs/06-cli-redesign.md:289-294`
- `internal/message/ingester.go:238-244`
- `internal/message/ingester.go:318-343`

初审问题：蓝图要求 daemon 写 `message_mentions` 前“过滤出该 room 当前成员”。此前 `resolveMentions` 只按 `accounts.bot_user_id` 解析账号，不与 `Memberships.ListByRoom(room.ID)` 做交集。

复审结论：已修复。`ingestNew` 现在复用当前 room members 构造 `memberSet`，`resolveMentions` 只返回当前成员对应的 account id。

覆盖测试：

- `internal/message/ingest_test.go::TestIngestDropsMentionsForNonMembers`

## Added Test

新增/复审覆盖的独立测试：

- `internal/message/ingest_test.go::TestIngestPersistsDiscordMentions`
- `internal/message/ingest_test.go::TestIngestDropsMentionsForNonMembers`
- `internal/message/ingest_test.go::TestIngestConflictPathMergesMentionMetadata`
- `internal/message/ingest_test.go::TestApplySendMetadataDoesNotClobberMentionEveryone`
- `internal/store/sqlite/message_mention_repo_test.go::TestMessageMentionRepoAddForMessageUnions`
- `internal/store/sqlite/message_repo_test.go::TestMergeMentionEveryoneOrSemantics`
- `internal/store/sqlite/message_repo_test.go::TestMergeMentionEveryoneNotFoundOnMissingID`

覆盖点：

- `MentionEveryone` 从 `bot.Message` 持久化到 `messages.mention_everyone`
- `MentionedBotUserIDs` 通过 `bot_user_id` 解析到 `message_mentions`
- duplicate mention 去重
- 未知 Discord user id 不写入 mention 表
- 非 room member mention 不写入 mention 表
- send path / ingester conflict 时 mention metadata 合并不丢失
- `mention_everyone=true` 不会被后续 send metadata 写入 false 覆盖

## Verification

执行过：

```bash
env GOCACHE=/tmp/agentchat-gocache go test ./internal/message ./internal/store/sqlite ./internal/state ./internal/api -count=1
env GOCACHE=/tmp/agentchat-gocache go test ./internal/api -count=1
env GOCACHE=/tmp/agentchat-gocache go test ./internal/message ./internal/store/sqlite ./internal/state -count=1
env GOCACHE=/tmp/agentchat-gocache go test ./... -count=1
```

说明：第一次不带 `GOCACHE=/tmp/agentchat-gocache` 的 `-count=1` 测试遇到默认 Go build cache 只读；`internal/api` 在普通沙箱内还会因 `httptest` 监听本地端口被拒绝。改用 `/tmp` cache 并提升本地监听权限后，全仓库测试通过。
