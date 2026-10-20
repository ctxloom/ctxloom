package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// markersHarness is a tree of the session and three children, the most
// recently active first, and a queue the test parks requests on.
func markersHarness(t *testing.T, w int) (*splitHarness, *splitPort, *fakeQueue) {
	t.Helper()
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	port := newSplitPort()
	port.child("busy-crow", "coder", selfHarp, coord.StateExecuting).LastEventAt = t0.Add(3 * time.Minute)
	port.child("mid-wren", "finder", selfHarp, coord.StateIdle).LastEventAt = t0.Add(2 * time.Minute)
	port.child("calm-swan", "reviewer", selfHarp, coord.StateIdle).LastEventAt = t0.Add(time.Minute)
	q := newFakeQueue()
	port.queue = q
	h := newSplitHarness(t, port).sized(w, 12)
	require.Equal(t, []string{selfHarp + "@0", "busy-crow@1", "mid-wren@1", "calm-swan@1"}, rosterOrder(h.s.rows))
	return h, port, q
}

// treeText is the tree's rows, styles stripped and trailing blanks cut.
func treeText(h *splitHarness) []string {
	lines := h.s.treeLines(rosterPaneWidth, len(h.s.rows))
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return lines
}

// A row whose agent waits on approvals says so in place of its activity
// glyph — a red ! — with the count past one at its right edge beside its
// unread mail as ✉ N, which a long name is cut short to keep; it sorts to
// the top of
// its level, ahead of the activity order. The footer keeps the total. The
// asker is the queue's own attribution (each request's From harp).
func TestSplit_RowMarkersAndApprovalsFirst(t *testing.T) {
	h, port, q := markersHarness(t, 100)
	q.add(bashReq("r1", "calm-swan", "ls", time.Hour))
	q.add(bashReq("r2", "calm-swan", "pwd", time.Hour))
	q.add(bashReq("r3", "mid-wren", "date", time.Hour))
	port.setRuns(func(runs []coord.RunInfo) { runs[2].Unread = 4 })
	h.send(rosterRefreshMsg{debounced: true})
	h.send(approvalsCountMsg{n: 3})

	assert.Equal(t, []string{selfHarp + "@0", "mid-wren@1", "calm-swan@1", "busy-crow@1"}, rosterOrder(h.s.rows),
		"rows waiting on an approval first, by activity among themselves")
	tree := treeText(h)
	assert.Equal(t, "  ! mid-wren·finder", tree[1])
	assert.Equal(t, "  ! calm-swan·revi… !2 ✉ 4", tree[2])
	assert.Contains(t, h.s.treeLine(1, rosterPaneWidth, false, h.clk.Now()), styleWarn.Render(pendingGlyph), "drawn to catch the eye")
	assert.NotContains(t, h.s.treeLine(1, rosterPaneWidth, true, h.clk.Now()), styleWarn.Render(pendingGlyph), "but not inside a selected row's own style")
	assert.True(t, h.panelHas(pendingGlyph+" 3 approvals"), "the footer keeps the total")

	assert.Equal(t, "  ! calm-swan·reviewer            !2 ✉ 4", strings.TrimRight(ansi.Strip(h.s.treeLine(2, 40, false, h.clk.Now())), " "))

	q.resolve("r1")
	q.resolve("r2")
	q.resolve("r3")
	h.send(approvalsCountMsg{n: 0})
	assert.Equal(t, []string{selfHarp + "@0", "busy-crow@1", "mid-wren@1", "calm-swan@1"}, rosterOrder(h.s.rows), "back to the activity order")
	assert.NotContains(t, strings.Join(treeText(h), "\n"), pendingGlyph)
}

// The selection is its harp through the approvals' reorder too.
func TestSplit_ApprovalsReorderKeepsTheSelection(t *testing.T) {
	h, _, q := markersHarness(t, 100)
	h.typeBytes("\x1d")
	h.keys("j")
	require.Equal(t, "busy-crow", selectedHarp(h))
	q.add(bashReq("r1", "calm-swan", "ls", time.Hour))
	h.send(approvalsCountMsg{n: 1})
	assert.Equal(t, "busy-crow", selectedHarp(h))
	h.keys("k")
	assert.Equal(t, "calm-swan", selectedHarp(h), "up from where busy-crow now is: calm-swan, raised above it")
}

// On a terminal too narrow for the tree, ! stands in for a waiting row's
// state glyph.
func TestSplit_GlyphColumnFlagsAWaitingRow(t *testing.T) {
	h, _, q := markersHarness(t, rosterPaneWidth+minPaneCols)
	q.add(bashReq("r1", "calm-swan", "ls", time.Hour))
	h.send(approvalsCountMsg{n: 1})
	col := h.s.glyphColumn(12)
	var got []string
	for _, g := range col[:4] {
		got = append(got, strings.TrimRight(ansi.Strip(g), " "))
	}
	assert.Equal(t, []string{"●", pendingGlyph, "●", "◐"}, got, "self, calm-swan (waiting, first), busy-crow, mid-wren")
}
