package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

func memberSet() []RoomMember {
	return []RoomMember{
		{AccountID: "acc-alice", Name: "alice", BotUserID: "u-alice"},
		{AccountID: "acc-bob", Name: "bob", BotUserID: "u-bob"},
		// carol is a member but has never come online — no BotUserID.
		{AccountID: "acc-carol", Name: "carol", BotUserID: ""},
	}
}

func TestParseMentionsRewritesKnownMembers(t *testing.T) {
	got, err := ParseMentions("hi @alice please take a look", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "hi <@u-alice> please take a look", got.RewrittenContent)
	assert.Equal(t, []string{"u-alice"}, got.BotUserIDs)
	assert.Equal(t, []string{"acc-alice"}, got.MentionedAccountIDs)
	assert.False(t, got.Everyone)
}

func TestParseMentionsRewritesMultipleKnownMembers(t *testing.T) {
	got, err := ParseMentions("@alice and @bob - sync up?", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "<@u-alice> and <@u-bob> - sync up?", got.RewrittenContent)
	assert.Equal(t, []string{"u-alice", "u-bob"}, got.BotUserIDs)
	assert.Equal(t, []string{"acc-alice", "acc-bob"}, got.MentionedAccountIDs)
}

func TestParseMentionsDedupesRepeatedMentions(t *testing.T) {
	got, err := ParseMentions("@alice @alice @alice", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "<@u-alice> <@u-alice> <@u-alice>", got.RewrittenContent,
		"all three occurrences rewrite, but allow-lists dedupe")
	assert.Equal(t, []string{"u-alice"}, got.BotUserIDs)
	assert.Equal(t, []string{"acc-alice"}, got.MentionedAccountIDs)
}

func TestParseMentionsKeepsEveryoneVerbatimAndFlags(t *testing.T) {
	got, err := ParseMentions("@everyone meeting now", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "@everyone meeting now", got.RewrittenContent,
		"the literal @everyone stays so the rendered message still reads naturally")
	assert.True(t, got.Everyone)
	assert.Empty(t, got.BotUserIDs)
	assert.Empty(t, got.MentionedAccountIDs)
}

func TestParseMentionsHereIsNotPromoted(t *testing.T) {
	got, err := ParseMentions("@here heads up", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "@here heads up", got.RewrittenContent)
	assert.False(t, got.Everyone, "@here is intentionally not supported as a ping")
	assert.Empty(t, got.BotUserIDs)
}

func TestParseMentionsUnknownNameSurvivesVerbatim(t *testing.T) {
	got, err := ParseMentions("hello @nobody-here", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "hello @nobody-here", got.RewrittenContent)
	assert.Empty(t, got.BotUserIDs)
	assert.Empty(t, got.MentionedAccountIDs)
}

func TestParseMentionsMemberWithoutBotUserIDStaysLiteral(t *testing.T) {
	// carol is in the room but never came online → no Discord id to
	// ping. Don't fabricate one; leave the literal alone.
	got, err := ParseMentions("@carol around?", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "@carol around?", got.RewrittenContent)
	assert.Empty(t, got.BotUserIDs)
	assert.Empty(t, got.MentionedAccountIDs)
}

func TestParseMentionsRejectsRawIDMention(t *testing.T) {
	_, err := ParseMentions("hi <@123456789012345678>", memberSet())
	require.Error(t, err)
	ec, _ := errcode.As(err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.InvalidArgument, ec.Code)
}

func TestParseMentionsEmptyContent(t *testing.T) {
	got, err := ParseMentions("", memberSet())
	require.NoError(t, err)
	assert.Equal(t, "", got.RewrittenContent)
	assert.Empty(t, got.BotUserIDs)
	assert.False(t, got.Everyone)
}

func TestParseMentionsEmptyMembersOnlyResolvesEveryone(t *testing.T) {
	got, err := ParseMentions("@alice and @everyone", nil)
	require.NoError(t, err)
	assert.Equal(t, "@alice and @everyone", got.RewrittenContent,
		"@alice has no member to resolve against; @everyone still flags")
	assert.True(t, got.Everyone)
	assert.Empty(t, got.BotUserIDs)
}

func TestParseMentionsDotInNameIsHonored(t *testing.T) {
	members := []RoomMember{
		{AccountID: "acc-alice.bot", Name: "alice.bot", BotUserID: "u-ab"},
	}
	got, err := ParseMentions("ping @alice.bot now", members)
	require.NoError(t, err)
	assert.Equal(t, "ping <@u-ab> now", got.RewrittenContent)
	assert.Equal(t, []string{"u-ab"}, got.BotUserIDs)
}
