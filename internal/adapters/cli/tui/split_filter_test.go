package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// contextHarps are the harps of the rows shown only for their place in the
// tree.
func contextHarps(h *splitHarness) map[string]bool {
	out := map[string]bool{}
	for _, r := range h.s.rows {
		if r.Context {
			out[r.Harp] = true
		}
	}
	return out
}

// f cycles the status filter — all, active, waiting, ended — under the age
// window; the run's own session and a shown row's parent stay as dim
// context, and the footer says which filter shows and how much it hides.
func TestSplit_FCyclesTheStatusFilter(t *testing.T) {
	h, _ := windowHarness(t)
	h.typeBytes("\x1d")

	h.keys("f")
	assert.ElementsMatch(t, []string{
		selfHarp, "stale-asker", "stale-mailed", "stale-parent", "busy-grandkid", "stale-live",
	}, rowHarps(h.s.rows), "active, and new or mattering now")
	assert.Equal(t, map[string]bool{"stale-parent": true}, contextHarps(h), "an aged parent is context")
	assert.Equal(t, []string{" active · 7d · +4 hidden"}, panelLinesWith(h, "hidden"))

	h.keys("f")
	assert.Equal(t, []string{selfHarp, "stale-asker"}, rowHarps(h.s.rows))
	assert.Equal(t, map[string]bool{selfHarp: true}, contextHarps(h), "the run's own session always shows")
	assert.Equal(t, []string{" waiting · 7d · +8 hidden"}, panelLinesWith(h, "hidden"))

	h.keys("f")
	assert.Equal(t, []string{selfHarp, "fresh-ended"}, rowHarps(h.s.rows), "a run ended past the window stays hidden")

	h.keys("f")
	assert.Len(t, h.s.rows, 7, "back to every status")
	assert.Equal(t, map[string]bool{"stale-parent": true}, contextHarps(h), "a parent past the window is context too")
	assert.Equal(t, []string{" +3 older than 7d"}, panelLinesWith(h, "older"))
}

// w cycles the age window up from ui.list_window, to no window and round to
// the shortest; with every status and nothing hidden the footer says nothing.
func TestSplit_WCyclesTheAgeWindow(t *testing.T) {
	h, _ := windowHarness(t)
	h.typeBytes("\x1d")

	h.keys("w")
	assert.Len(t, h.s.rows, 10, "no window: every row")
	assert.Empty(t, panelLinesWith(h, "older"))
	h.keys("f")
	assert.Equal(t, []string{" active · +3 hidden"}, panelLinesWith(h, "hidden"), "no window: the filter alone")
	h.keys("f", "f", "f")

	h.keys("w")
	assert.Equal(t, time.Hour, h.s.listWindow)
	assert.NotContains(t, rowHarps(h.s.rows), "fresh-ended", "a day old is past an hour")
	assert.Contains(t, rowHarps(h.s.rows), "stale-mailed", "mail shows whatever its age")
	assert.Equal(t, []string{" +4 older than 1h"}, panelLinesWith(h, "older"))
}

// A context row is drawn dim; a row the filter passes is not.
func TestSplit_ContextRowsAreDim(t *testing.T) {
	h, _ := windowHarness(t)
	h.typeBytes("\x1d")
	h.keys("f")
	now := h.clk.Now()
	for i, r := range h.s.rows {
		line := h.s.treeLine(i, rosterPaneWidth, false, now)
		switch r.Harp {
		case "stale-parent":
			assert.Equal(t, styleDim.Render(ansi.Strip(line)), line, r.Harp)
		case "stale-live":
			assert.Equal(t, ansi.Strip(line), line, r.Harp)
		}
	}
}
