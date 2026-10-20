package transcript

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// replayShape renders events as a compact sequence: an entry as its content,
// a boundary as "[from,to)".
func replayShape(events []*WatchEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		switch {
		case ev.Entry != nil:
			out = append(out, ev.Entry.Content)
		case ev.Boundary != nil:
			out = append(out, fmt.Sprintf("[%d,%d)", ev.Boundary.FromIndex, ev.Boundary.ToIndex))
		default:
			out = append(out, "heartbeat")
		}
	}
	return out
}

func entry(typ agent.SessionEntryType, content string, sidechain bool) agent.SessionEntry {
	return agent.SessionEntry{Type: typ, Content: content, Sidechain: sidechain}
}

// A whole transcript replays every entry in order, each turn closed by a
// boundary: a turn opens at a main-thread user entry and runs to the next.
func TestReplayEvents_ClosesEachTurnWithABoundary(t *testing.T) {
	got := ReplayEvents([]agent.SessionEntry{
		entry(agent.EntryTypeUser, "q1", false),
		entry(agent.EntryTypeAssistant, "a1", false),
		entry(agent.EntryTypeUser, "q2", false),
		entry(agent.EntryTypeToolUse, "t", false),
		entry(agent.EntryTypeAssistant, "a2", false),
	})
	assert.Equal(t, []string{"q1", "a1", "[0,2)", "q2", "t", "a2", "[2,5)"}, replayShape(got))
}

// A subagent's user entry is inside its parent's turn, not the start of one.
func TestReplayEvents_ASubagentsPromptDoesNotOpenATurn(t *testing.T) {
	got := ReplayEvents([]agent.SessionEntry{
		entry(agent.EntryTypeUser, "q1", false),
		entry(agent.EntryTypeUser, "sub", true),
		entry(agent.EntryTypeAssistant, "a1", false),
	})
	assert.Equal(t, []string{"q1", "sub", "a1", "[0,3)"}, replayShape(got))
}

// Material ahead of the first prompt is a turn of its own; an empty
// transcript replays nothing.
func TestReplayEvents_MaterialBeforeTheFirstPromptIsItsOwnTurn(t *testing.T) {
	got := ReplayEvents([]agent.SessionEntry{
		entry(agent.EntryTypeSystem, "s", false),
		entry(agent.EntryTypeUser, "q1", false),
	})
	assert.Equal(t, []string{"s", "[0,1)", "q1", "[1,2)"}, replayShape(got))
	assert.Empty(t, ReplayEvents(nil))
}
