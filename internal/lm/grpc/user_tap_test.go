package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// U059-F11: the inbound user-turn tap required msg.Text != "", so a
// ContentBlocks-only turn was delivered to the engine but recorded NOWHERE —
// the transcript shows an assistant reply to a prompt that never appears.
func TestUserTapEntry_ContentBlocksOnlyTurnIsRecorded(t *testing.T) {
	entry := userTapEntry(agent.ChatMessage{ContentBlocks: []agent.ContentBlock{
		{Kind: "text", Text: "what is in this image?"},
		{Kind: "image"},
	}})
	require.NotNil(t, entry, "a turn the engine received must appear in the transcript")
	assert.Equal(t, agent.EntryTypeUser, entry.Type)
	assert.Contains(t, entry.Content, "what is in this image?")
	assert.Len(t, entry.ContentBlocks, 2, "the structured blocks ride along, not just the flattening")
}

// A blocks-only turn with no text at all still leaves a trace rather than an
// empty entry that reads as "the user said nothing".
func TestUserTapEntry_NonTextBlocksLeaveATrace(t *testing.T) {
	entry := userTapEntry(agent.ChatMessage{ContentBlocks: []agent.ContentBlock{{Kind: "image"}}})
	require.NotNil(t, entry)
	assert.NotEmpty(t, entry.Content, "an image-only turn must not record as an empty user entry")
	assert.Contains(t, entry.Content, "image")
}

// Plain text is unchanged, and a wholly empty message is still recorded as
// nothing (there is genuinely no turn to record).
func TestUserTapEntry_TextAndEmpty(t *testing.T) {
	entry := userTapEntry(agent.ChatMessage{Text: "hello"})
	require.NotNil(t, entry)
	assert.Equal(t, "hello", entry.Content)
	assert.Nil(t, userTapEntry(agent.ChatMessage{}))
}
