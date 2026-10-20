package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// selectedHarp is the harp of the row the tree draws as selected, or "".
func selectedHarp(h *splitHarness) string {
	visible := h.s.visibleRows()
	if i := h.s.visibleSel(visible); i >= 0 {
		return h.s.rows[visible[i]].Harp
	}
	return ""
}

// The prefix hands the tree the keyboard with its selection on the agent
// the pane is showing: at first the run's own session — present even when
// the session index has not listed it (yet), and ahead of rows more recently
// active — then whichever child the pane was re-targeted to.
func TestSplit_EnteringTheListStartsOnTheActiveSession(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	port := newSplitPort()
	port.sessions = []frontend.Session{
		{Harp: "busier-older-run", State: frontend.SessionEnded, LastActivity: t0.Add(time.Hour)},
		{Harp: "quiet-older-run", State: frontend.SessionEnded, LastActivity: t0},
	}
	port.child("brave-heron", "coder", selfHarp, coord.StateIdle)
	port.child("sleepy-ibis", "reviewer", selfHarp, coord.StateIdle)
	h := newSplitHarness(t, port).sized(100, 12)
	require.Equal(t, []string{selfHarp + "@0", "brave-heron@1", "sleepy-ibis@1", "busier-older-run@0", "quiet-older-run@0"}, rosterOrder(h.s.rows),
		"the run's own session is a row, pinned on top, its children under it")

	h.typeBytes("\x1d")
	require.Equal(t, focusList, h.s.focus)
	assert.Equal(t, selfHarp, selectedHarp(h), "the list starts on the session the pane shows")
	h.keys("j")
	assert.Equal(t, "brave-heron", selectedHarp(h), "and moves on from there, not from the top")

	h.keys("j", "esc")
	require.Equal(t, focusPane, h.s.focus)
	require.Equal(t, "sleepy-ibis", h.s.sel)
	port.setRuns(func(runs []coord.RunInfo) { runs[1].LastEventAt = t0.Add(2 * time.Hour) })
	h.send(rosterRefreshMsg{debounced: true})
	require.Equal(t, "sleepy-ibis@1", rosterOrder(h.s.rows)[1], "re-ordered by activity")
	h.typeBytes("\x1d")
	assert.Equal(t, "sleepy-ibis", selectedHarp(h), "back in the list, on the child the pane shows, wherever it now is")
}
