package bot

import (
	"regexp"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// RoomMember is a flattened view of an account-as-room-member, used
// only by the outbound mention parser (M9 Phase 2). The send-path
// handler in internal/api/v1/messages.go assembles this slice from
// MembershipRepo + AccountRepo before calling Provider.SendMessage so
// the bot adapter never has to reach back into the store. Provider
// implementations and tests can synthesize members as needed.
type RoomMember struct {
	AccountID string
	Name      string
	BotUserID string
}

// ParsedMentions is the output of ParseMentions: a rewritten content
// suitable for Discord's API (with `@<name>` replaced by `<@id>`), the
// allow-lists the caller hands to AllowedMentions, and the account-id
// set the daemon writes into message_mentions.
type ParsedMentions struct {
	// RewrittenContent is the content with every resolved `@<name>`
	// substituted for `<@bot_user_id>`. Unresolved `@<name>` tokens
	// (names not in the room or empty BotUserID) survive verbatim so
	// the message still reads naturally; `@everyone` and `@here` are
	// always preserved verbatim.
	RewrittenContent string
	// BotUserIDs is the deduplicated list of bot_user_ids the caller
	// should pass to AllowedMentions.Users so Discord actually pings
	// those users. Order matches first-occurrence in the content.
	BotUserIDs []string
	// Everyone is true when content contains `@everyone`. Callers map
	// this to AllowedMentions.Parse=[everyone].
	Everyone bool
	// MentionedAccountIDs is the deduplicated list of agentchat
	// account_ids the daemon should fan into message_mentions. Order
	// mirrors BotUserIDs.
	MentionedAccountIDs []string
}

// rawMentionRe matches Discord's native mention syntax — `<@id>`,
// `<@!id>`, `<@&roleid>`, `<#channelid>`. We reject any of them on the
// outbound path because agents shouldn't be hand-writing platform
// IDs — they go through `@<name>` so the daemon can validate room
// membership and produce a stable Discord ↔ agentchat audit trail.
// The character class is deliberately loose (anything-non-`>`) so
// test fixtures using mock IDs like `<@u-someone>` are rejected the
// same way real Discord snowflakes would be.
var rawMentionRe = regexp.MustCompile(`<[@#][!&]?[^>\s]+>`)

// nameMentionRe matches a leading `@` followed by a Discord-shaped
// username token. The character class deliberately tracks Discord's
// real username constraints ([a-zA-Z0-9._-]) so we don't accidentally
// chop names containing `.` or `_`. Trailing punctuation in the
// original content (commas, periods after the token) is the user's
// problem — the same rule applies in the Discord client.
var nameMentionRe = regexp.MustCompile(`@[a-zA-Z0-9._-]+`)

// ParseMentions scans content for @-mention tokens and produces the
// rewritten form plus allow-list slices. It is platform-shaped (Discord
// native `<@id>` rewrites) but lives in package bot so the API handler
// can call it without depending on the Discord adapter.
//
// Rules (mirrors docs/06-cli-redesign.md §4.3):
//   - Raw `<@123456789>` → reject (InvalidArgument).
//   - `@everyone` → set Everyone=true, keep verbatim.
//   - `@here` → keep verbatim, no allow-list entry.
//   - `@<name>` matching a room member's Name → rewrite to
//     `<@BotUserID>` and record on both allow-lists.
//   - `@<name>` not in the room or whose member has empty BotUserID →
//     keep verbatim (Discord client behaviour for an unresolved @).
//   - Duplicate matches per account_id / bot_user_id are deduplicated.
//
// An empty `members` slice means "no one is in this room from
// agentchat's view"; only `@everyone` can still be resolved.
func ParseMentions(content string, members []RoomMember) (ParsedMentions, error) {
	if rawMentionRe.MatchString(content) {
		return ParsedMentions{}, errcode.New(errcode.InvalidArgument,
			"content contains raw Discord <@id> mention; use @<name> instead")
	}
	byName := make(map[string]RoomMember, len(members))
	for _, m := range members {
		if m.Name == "" {
			continue
		}
		byName[m.Name] = m
	}
	var (
		out      ParsedMentions
		seenAcc  = make(map[string]struct{})
		seenUser = make(map[string]struct{})
	)
	rewritten := nameMentionRe.ReplaceAllStringFunc(content, func(match string) string {
		token := match[1:] // strip the leading '@'
		switch token {
		case "everyone":
			out.Everyone = true
			return match
		case "here":
			// Documented as out of scope (Q9): we don't promote @here
			// to a real ping. Preserve the literal so humans can still
			// see the intent in the rendered message.
			return match
		}
		m, ok := byName[token]
		if !ok || m.BotUserID == "" {
			return match
		}
		if _, dup := seenUser[m.BotUserID]; !dup {
			seenUser[m.BotUserID] = struct{}{}
			out.BotUserIDs = append(out.BotUserIDs, m.BotUserID)
		}
		if _, dup := seenAcc[m.AccountID]; !dup {
			seenAcc[m.AccountID] = struct{}{}
			out.MentionedAccountIDs = append(out.MentionedAccountIDs, m.AccountID)
		}
		return "<@" + m.BotUserID + ">"
	})
	out.RewrittenContent = rewritten
	return out, nil
}
