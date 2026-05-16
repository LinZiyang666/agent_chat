# M9 Phase 2 Re-Review

日期：2026-05-16

范围：复审当前工作区内尚未提交的 M9 Phase 2 改动（包含暂存区和暂存区外），并对照 `docs/06-cli-redesign.md`。

## 结论

审查结论：**通过**。

本轮复审确认上一轮列出的 3 个问题均已修复：

- `ReadRoom` 的 room-level hydration 已不再依赖返回消息非空，空消息结果也会填充 `room.current_announcement_id`。
- CLI TTY 输出已优先使用 `author_name` / `display_content`，并保留 `author_account_id` / `content` fallback。
- `set-discord` 已兼容 `?force_rename=true` query；非法 query 值会返回 `INVALID_ARGUMENT`。

审查过程中发现 3 个 Go 文件未 `gofmt`，已做纯机械格式化：

- `internal/api/v1/discord.go`
- `internal/api/v1/messages.go`
- `internal/api/v1/types.go`

## Findings

未发现新的阻塞问题。

## 覆盖确认

新增测试覆盖了上一轮问题：

- `TestReadRoomCurrentAnnouncementIDPresentWhenEmpty`
- `TestSetDiscordForceRenameViaQueryString`
- `TestSetDiscordForceRenameQueryRejectsBogusValue`

既有 M9 Phase 2 测试也继续覆盖：

- username mismatch 返回 `CONFLICT`
- `ForceRename` 调用 prober rename
- `bot_user_id` 落库
- `ReadRoomResponse` 补齐 `author_name` / `display_content` / `read_at` / `current_announcement_id`
- `--before` 默认 limit 为 50

## Verification

执行过：

```bash
gofmt -l internal/api/v1/discord.go internal/api/v1/messages.go internal/api/v1/types.go
git diff --check
env GOCACHE=/tmp/agentchat-gocache go test ./... -count=1
```

结果：全部通过。

说明：全量测试需要创建本地 TCP/Unix socket，因此在允许本地 listener 的环境中执行。
