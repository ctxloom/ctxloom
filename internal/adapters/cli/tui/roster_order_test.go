package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// rosterOrder is rows as harp@depth.
func rosterOrder(rows []RosterRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Harp + "@" + string(rune('0'+r.Depth))
	}
	return out
}

// Re-ordering the tree never moves the selection: it stays on its harp, and
// the pane keeps that harp's feed.
func TestSplit_SelectionSurvivesAReorder(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateIdle).LastEventAt = t0.Add(2 * time.Minute)
	port.child("sleepy-ibis", "reviewer", selfHarp, coord.StateIdle).LastEventAt = t0.Add(time.Minute)
	h := newSplitHarness(t, port).sized(100, 10)
	require.Equal(t, []string{selfHarp + "@0", "brave-heron@1", "sleepy-ibis@1"}, rosterOrder(h.s.rows))

	h.typeBytes("\x1d")
	h.keys("j", "j")
	require.Equal(t, "sleepy-ibis", h.s.sel)
	port.setRuns(func(runs []coord.RunInfo) { runs[1].LastEventAt = t0.Add(3 * time.Minute) })
	h.send(rosterRefreshMsg{debounced: true})

	require.Equal(t, []string{selfHarp + "@0", "sleepy-ibis@1", "brave-heron@1"}, rosterOrder(h.s.rows))
	assert.Equal(t, "sleepy-ibis", h.s.sel, "the selection is its harp, not its index")
	selected := panelLinesWith(h, "sleepy-ibis")
	require.Len(t, selected, 1)
	assert.Equal(t, 1, h.s.selIndex(), "drawn where the harp now is")
	assert.Equal(t, []string{"brave-heron", "sleepy-ibis"}, port.watchedHarps(), "the pane keeps its feed: no new watch")
	h.keys("j")
	assert.Equal(t, "brave-heron", h.s.sel, "moving on goes from where the harp now is")
}

// Entering the list over a live session lands on its own row, which the
// coordinator lists as its own parent.
func TestSplit_EnteringTheListOverASelfParentedOwnerRun(t *testing.T) {
	port := newSplitPort()
	port.sessions = append(port.sessions, frontend.Session{Harp: "older-run", State: frontend.SessionEnded, LastActivity: time.Now()})
	port.child(selfHarp, selfHarp, selfHarp, coord.StateExecuting)
	port.child("brave-heron", "coder", selfHarp, coord.StateIdle)
	h := newSplitHarness(t, port).sized(100, 12)
	h.typeBytes("\x1d")
	assert.Equal(t, selfHarp, selectedHarp(h))
	assert.Equal(t, []string{selfHarp + "@0", "brave-heron@1", "older-run@0"}, rosterOrder(h.s.rows))
}
