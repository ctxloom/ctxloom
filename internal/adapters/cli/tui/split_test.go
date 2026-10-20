package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// renderInto draws the split's rows on a reference terminal of its size and
// returns that terminal's screen: what the human would see.
func renderInto(t *testing.T, s *Split) termemu.Screen {
	t.Helper()
	ref := termemu.NewXVT(s.w, s.h, newEngineSink(), termemu.Callbacks{})
	defer func() { _ = ref.Close() }()
	_, err := ref.Write([]byte(strings.Join(s.render(), "\r\n")))
	require.NoError(t, err)
	return ref.Snapshot()
}

func rowText(s termemu.Screen, y, from, to int) string {
	var b strings.Builder
	for x := from; x < to && x < s.Cols; x++ {
		c := s.Cells[y][x]
		if c.Width == 0 {
			continue
		}
		if c.Content == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// The headless snapshot: the panel's rows, the divider in its column on
// every row, the engine's cells offset into the pane exactly, the engine's
// cursor offset with them, and the footer.
func TestSplit_SnapshotComposesPanelDividerAndPane(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	port.child("sleepy-ibis", "reviewer", selfHarp, coord.StateIdle)
	h := newSplitHarness(t, port).sized(100, 12)
	h.engineWrites("\x1b[31mhello\x1b[0m 日本 ✅ \x1b[1;4mbold\x1b[0m\r\nsecond line\x1b[3;5H")

	panelW, paneW := h.s.layout()
	require.Equal(t, rosterPaneWidth, panelW)
	require.Equal(t, 100-rosterPaneWidth-1, paneW)
	require.Equal(t, [][2]uint16{{12, uint16(paneW)}}, h.engine.resizes(), "the engine is sized to the pane, once")

	scr := renderInto(t, h.s)
	inner := h.pane.Snapshot()
	require.Equal(t, paneW, inner.Cols)
	for y := range 12 {
		assert.Equal(t, "│", scr.Cells[y][panelW].Content, "row %d: the divider sits in column %d", y, panelW)
		for x := 0; x < paneW; x++ {
			want, got := inner.Cells[y][x], scr.Cells[y][panelW+1+x]
			if !assert.Equal(t, want.Content, got.Content, "cell (%d,%d)", x, y) ||
				!assert.Equal(t, want.Width, got.Width, "cell (%d,%d) width", x, y) ||
				!assert.True(t, want.Style.Equal(&got.Style), "cell (%d,%d) style %v vs %v", x, y, want.Style, got.Style) {
				return
			}
		}
	}

	assert.Equal(t, " "+selfHarp, rowText(scr, 0, 0, panelW), "the header names the session")
	assert.Equal(t, " claude-code·haiku", rowText(scr, 1, 0, panelW))
	tree := []string{rowText(scr, 2, 0, panelW), rowText(scr, 3, 0, panelW), rowText(scr, 4, 0, panelW)}
	assert.Equal(t, []string{" ● " + selfHarp, "  ● brave-heron·coder", "  ◐ sleepy-ibis·reviewer"}, tree)
	assert.Equal(t, " ^] agents", rowText(scr, 11, 0, panelW), "the footer, with the pane focused")

	c := h.s.View().Cursor
	require.NotNil(t, c)
	assert.Equal(t, panelW+1+4, c.X, "the engine's cursor column, offset into the pane")
	assert.Equal(t, 2, c.Y)

	h.typeBytes("\x1d")
	assert.Nil(t, h.s.View().Cursor, "the tree has focus: no cursor in the pane")
	scr = renderInto(t, h.s)
	var panel []string
	for y := range 12 {
		panel = append(panel, rowText(scr, y, 0, panelW))
	}
	assert.Contains(t, strings.Join(panel, "\n"), " j/k move", "the tree's key hints, with the tree focused")
	assert.NotContains(t, strings.Join(panel, "\n"), "^] agents")
}

func TestSplit_NarrowTerminalCollapsesThePanelToAGlyphColumn(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	port.child("sleepy-ibis", "reviewer", selfHarp, coord.StateQueued)
	h := newSplitHarness(t, port).sized(rosterPaneWidth+minPaneCols, 8)
	h.engineWrites("engine")

	panelW, paneW := h.s.layout()
	require.Equal(t, glyphColumnWidth, panelW)
	require.Equal(t, rosterPaneWidth+minPaneCols-glyphColumnWidth-1, paneW)
	scr := renderInto(t, h.s)
	for y := range 8 {
		assert.Equal(t, "│", scr.Cells[y][panelW].Content, "row %d", y)
	}
	assert.Equal(t, []string{"●", "●", "◌", "", "", "", "", ""}, glyphs(scr, 8))
	assert.Equal(t, "engine", rowText(scr, 0, panelW+1, scr.Cols))
	assert.Equal(t, glyphColumnWidth+1+6, h.s.View().Cursor.X)

	h.send(approvalsCountMsg{n: 2})
	assert.Equal(t, pendingGlyph, glyphs(renderInto(t, h.s), 8)[7], "approvals waiting show on the bottom row")

	// One column wider and the tree is back: no bottom bar either way.
	h.send(tea.WindowSizeMsg{Width: rosterPaneWidth + 1 + minPaneCols, Height: 8})
	panelW, _ = h.s.layout()
	assert.Equal(t, rosterPaneWidth, panelW)
	assert.Equal(t, [2]uint16{8, minPaneCols}, h.engine.resizes()[1])
}

func glyphs(s termemu.Screen, rows int) []string {
	out := make([]string, rows)
	for y := range rows {
		out[y] = strings.TrimSpace(rowText(s, y, 0, glyphColumnWidth))
	}
	return out
}

func TestSplit_EnterHandsTheSelectedRowItsPane(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	port.child("tiny-wren", "tester", "brave-heron", coord.StateIdle)
	h := newSplitHarness(t, port).sized(100, 12)
	tree := func() []string {
		var out []string
		for _, r := range h.screen()[2:5] {
			out = append(out, strings.TrimSpace(strings.SplitN(r, "│", 2)[0]))
		}
		return out
	}
	require.Equal(t, []string{"● " + selfHarp, "● brave-heron·coder", "◐ tiny-wren·tester"}, tree(), "no fold marks")

	h.typeBytes("\x1d")
	h.keys("j", "enter")
	assert.Equal(t, "brave-heron", h.s.sel)
	assert.Equal(t, focusPane, h.s.focus, "enter on a row with children hands focus to its pane")
	assert.Equal(t, []string{"● " + selfHarp, "● brave-heron·coder", "◐ tiny-wren·tester"}, tree(), "and hides nothing")
	h.keys("k", "k", "k")
	assert.Equal(t, "kkk", h.s.child.text, "with the pane focused, keys type into the child's line")
}

func TestActivityGlyph_EveryState(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		row  RosterRow
		want string
	}{
		{"launching", RosterRow{State: coord.StateQueued}, launchingGlyph},
		{"in a turn", RosterRow{State: coord.StateExecuting, LastActivity: now.Add(-time.Hour)}, "●"},
		{"idle", RosterRow{State: coord.StateIdle, LastActivity: now.Add(-idleDozeAfter + time.Second)}, "◐"},
		{"idle with no activity time", RosterRow{State: coord.StateIdle}, "◐"},
		{"dozing", RosterRow{State: coord.StateIdle, LastActivity: now.Add(-idleDozeAfter)}, dozingGlyph},
		{"held", RosterRow{State: coord.StateIdle, Hold: &coord.RunHold{Kind: coord.HoldKindHuman}, LastActivity: now.Add(-time.Hour)}, heldGlyph},
		{"held while queued", RosterRow{State: coord.StateQueued, Hold: &coord.RunHold{Kind: coord.HoldKindHuman}}, heldGlyph},
		{"ended", RosterRow{State: StateEnded, Cause: coord.CauseFinalReported}, "✓"},
		{"failed", RosterRow{State: StateEnded, Cause: coord.CauseRunnerLoss}, failedGlyph},
		{"live session", RosterRow{State: StateLive}, "●"},
		{"unknown session", RosterRow{State: StateUnknown}, "?"},
	} {
		assert.Equal(t, c.want, activityGlyph(c.row, now), c.name)
	}
}
