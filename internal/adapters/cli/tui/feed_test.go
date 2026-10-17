package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// itemsFromFeedEvent at the unit level. Boundaries and heartbeats never reach
// it: the port's feed carries entries and gaps only (operations'
// TestFrontendFeedEvent).

func TestItemsFromFeedEvent_GapProducesVisibleNotice(t *testing.T) {
	items := itemsFromFeedEvent(frontend.FeedEvent{Gap: 7})
	require.Len(t, items, 1)
	assert.Equal(t, "notice", items[0].role)
	assert.Contains(t, items[0].text, "7 live events dropped")
}

func TestItemsFromFeedEvent_NoEntryAndNoGapProduceNothing(t *testing.T) {
	assert.Empty(t, itemsFromFeedEvent(frontend.FeedEvent{}))
}

func TestItemsFromFeedEvent_EntryMapsEveryField(t *testing.T) {
	ev := frontend.FeedEvent{Entry: &agent.SessionEntry{
		Type:       "tool_use",
		Timestamp:  time.Unix(1700000000, 0),
		Content:    "look at this",
		ToolName:   "Bash",
		ToolInput:  []byte(`{"cmd":"ls"}`),
		ToolOutput: "file1\nfile2",
		IsError:    true,
		Sidechain:  true,
	}}
	items := itemsFromFeedEvent(ev)
	require.Len(t, items, 1)
	it := items[0]
	assert.Equal(t, "tool_use", it.role)
	assert.Equal(t, time.Unix(1700000000, 0), it.ts)
	assert.Equal(t, "look at this", it.text)
	assert.Equal(t, "Bash", it.toolName)
	assert.Equal(t, `{"cmd":"ls"}`, it.toolInput)
	assert.Equal(t, "file1\nfile2", it.toolOutput)
	assert.True(t, it.isError)
	assert.True(t, it.sidechain)
}
