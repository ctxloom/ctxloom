package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// F1 deliverable 5 (opportunistic unit gap): itemsFromFeedEvent's boundary/
// heartbeat suppression was untested at the unit level (only reachable
// indirectly, and only for the Gap/Entry branches, via model_test.go's
// pushEntry-driven Update tests).

func TestItemsFromFeedEvent_GapProducesVisibleNotice(t *testing.T) {
	items := itemsFromFeedEvent(operations.SessionFeedEvent{Gap: 7})
	require.Len(t, items, 1)
	assert.Equal(t, "notice", items[0].role)
	assert.Contains(t, items[0].text, "7 live events dropped")
}

func TestItemsFromFeedEvent_BoundarySuppressed(t *testing.T) {
	ev := operations.SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Boundary{
		Boundary: &pb.ResponseBoundary{FromIndex: 0, ToIndex: 2},
	}}}
	assert.Empty(t, itemsFromFeedEvent(ev), "a boundary marks a turn edge, not renderable content")
}

func TestItemsFromFeedEvent_HeartbeatSuppressed(t *testing.T) {
	ev := operations.SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Heartbeat{
		Heartbeat: &pb.Heartbeat{},
	}}}
	assert.Empty(t, itemsFromFeedEvent(ev), "a heartbeat is a keepalive, not renderable content")
}

func TestItemsFromFeedEvent_NilEventAndZeroGapProduceNothing(t *testing.T) {
	assert.Empty(t, itemsFromFeedEvent(operations.SessionFeedEvent{}))
}

func TestItemsFromFeedEvent_EntryMapsEveryField(t *testing.T) {
	ev := operations.SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{
		Type:          "tool_use",
		TimestampUnix: 1700000000,
		Content:       "look at this",
		ToolName:      "Bash",
		ToolInput:     []byte(`{"cmd":"ls"}`),
		ToolOutput:    "file1\nfile2",
		IsError:       true,
		Sidechain:     true,
	}}}}
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

func TestItemsFromFeedEvent_EntryVariantWithNilEntryProducesNothing(t *testing.T) {
	ev := operations.SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: nil}}}
	assert.Empty(t, itemsFromFeedEvent(ev))
}
