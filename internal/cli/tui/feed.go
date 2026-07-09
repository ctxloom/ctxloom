package tui

import (
	"fmt"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// feedItem is one rendered unit of the feed pane, converted from the
// observation feed's WatchEvent/SessionEntry vocabulary.
type feedItem struct {
	role       string // user | assistant | thinking | tool_use | tool_result | system | notice
	ts         time.Time
	text       string
	toolName   string
	toolInput  string // raw JSON
	toolOutput string
	isError    bool
	sidechain  bool
}

// itemsFromFeedEvent converts one feed event into zero or more items.
// Boundaries and heartbeats render nothing; a live-tap gap surfaces as a
// visible notice so the reader knows the view is incomplete.
func itemsFromFeedEvent(ev operations.SessionFeedEvent) []feedItem {
	if ev.Gap > 0 {
		return []feedItem{{
			role: "notice",
			text: fmt.Sprintf("… %d live events dropped (observer buffer overflow)", ev.Gap),
		}}
	}
	if ev.Event == nil {
		return nil
	}
	entry, ok := ev.Event.Event.(*pb.WatchEvent_Entry)
	if !ok || entry.Entry == nil {
		return nil
	}
	e := entry.Entry
	return []feedItem{{
		role:       e.Type,
		ts:         time.Unix(e.TimestampUnix, 0),
		text:       e.Content,
		toolName:   e.ToolName,
		toolInput:  string(e.ToolInput),
		toolOutput: e.ToolOutput,
		isError:    e.IsError,
		sidechain:  e.Sidechain,
	}}
}
