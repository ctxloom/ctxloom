package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

func TestSplit_NothingDrawnBeforeTheFirstSize(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	v := h.s.View()
	assert.Empty(t, v.Content)
	assert.Nil(t, v.Cursor)
	assert.True(t, v.AltScreen)
}

// Before the engine is sized its frame is the emulator's first size; the
// pane shows it fitted, blank below it, and once sized the frame's own rows
// are drawn as they are, not re-measured.
func TestSplit_TerminalRowsAreFittedOnlyUntilTheEngineIsSized(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.engineWrites("early")
	_, paneW := h.s.layout()
	rows := h.s.terminalLines(paneW, 30)
	require.Len(t, rows, 30)
	assert.True(t, strings.HasPrefix(ansi.Strip(rows[0]), "early"), "%q", rows[0])
	assert.Equal(t, paneW, ansi.StringWidth(rows[0]), "cut to the pane's width")
	assert.Equal(t, strings.Repeat(" ", paneW), rows[29], "below the emulator's rows: blank, full width")

	h.send(tea.BackgroundColorMsg{})
	h.frame()
	rows = h.s.terminalLines(paneW, 30)
	assert.Same(t, &h.s.paneLines[0], &rows[0], "a frame of the pane's size is drawn as serialized")
}

// The tree windows around the selection when it is taller than the panel,
// in the full panel and in the glyph column alike; j and k stop at its ends.
func TestSplit_TreeWindowsAroundTheSelection(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 8)
	rows := []RosterRow{{Harp: selfHarp}}
	for i := range 12 {
		rows = append(rows, RosterRow{Harp: fmt.Sprintf("kid-%02d", i), Parent: selfHarp, Depth: 1, State: coord.StateIdle})
	}
	h.send(rosterMsg{rows: rows})
	h.typeBytes("\x1d")
	h.keys("k")
	assert.Equal(t, selfHarp, h.s.sel, "k stops at the top")
	for range 9 {
		h.keys("j")
	}
	require.Equal(t, "kid-08", h.s.sel)
	// 8 rows: 2 header, footer lines, the rest tree; the selection sits in
	// the middle of the tree's window.
	tree := h.s.treeLines(rosterPaneWidth, 4)
	assert.Equal(t, []string{"kid-06", "kid-07", "kid-08", "kid-09"}, harps(tree))
	for range 10 {
		h.keys("j")
	}
	assert.Equal(t, "kid-11", h.s.sel, "j stops at the bottom")
	assert.Equal(t, []string{"kid-08", "kid-09", "kid-10", "kid-11"}, harps(h.s.treeLines(rosterPaneWidth, 4)), "the window ends at the last row")
	assert.Equal(t, 4, strings.Count(strings.Join(h.s.glyphColumn(4), ""), "◐"), "the glyph column windows the same way")
	assert.Contains(t, h.s.glyphColumn(4)[3], "\x1b[7m", "the selection is marked in the glyph column")
	assert.NotContains(t, h.s.glyphColumn(4)[2], "\x1b[7m")
}

func harps(lines []string) []string {
	var out []string
	for _, l := range lines {
		f := strings.Fields(ansi.Strip(l))
		out = append(out, f[len(f)-1])
	}
	return out
}

func TestSplit_DeepTreesIndentAtMostThreeLevels(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.send(rosterMsg{rows: []RosterRow{
		{Harp: selfHarp}, {Harp: "d1", Depth: 1}, {Harp: "d2", Depth: 2}, {Harp: "d3", Depth: 3}, {Harp: "d5", Depth: 5},
	}})
	tree := h.s.treeLines(rosterPaneWidth, 5)
	indent := func(l string) int { s := ansi.Strip(l); return len(s) - len(strings.TrimLeft(s, " ")) }
	assert.Equal(t, 1+3, indent(tree[3]), "depth 3: three columns of indent after the lead")
	assert.Equal(t, 1+3, indent(tree[4]), "depth 5 indents as depth 3")
	assert.Equal(t, 1+2, indent(tree[2]))
}

// The selected row is reversed while the tree has focus, and bold while
// the pane does.
func TestSplit_SelectionIsMarkedByFocus(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	sel := h.s.treeLines(rosterPaneWidth, 3)[0]
	assert.Contains(t, sel, "\x1b[1m")
	assert.NotContains(t, sel, "\x1b[7m")
	h.typeBytes("\x1d")
	sel = h.s.treeLines(rosterPaneWidth, 3)[0]
	assert.Contains(t, sel, "\x1b[7m")
}

func TestAgeLabel(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"}, {59 * time.Second, "59s"}, {time.Minute, "1m"},
		{59 * time.Minute, "59m"}, {time.Hour, "1h"}, {25 * time.Hour, "25h"},
	} {
		assert.Equal(t, c.want, ageLabel(c.d), "%v", c.d)
	}
}

func TestSplit_NoteLineOrder(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.s.status, h.s.note, h.s.rosterErr, h.s.errMsg = "status", "note", "roster", "failed"
	assert.Equal(t, "failed", h.s.noteLine())
	h.s.errMsg = ""
	assert.Equal(t, "roster", h.s.noteLine())
	h.s.rosterErr = ""
	assert.Equal(t, "note", h.s.noteLine())
	h.s.note = ""
	assert.Equal(t, "status", h.s.noteLine())
	h.s.status = "\x1b[31mred\x07"
	assert.NotContains(t, h.s.noteLine(), "\x1b", "a note is made display-safe")
}

func TestSplit_InputLineHintsOnlyWhileEmpty(t *testing.T) {
	h, _ := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j", "esc")
	assert.Contains(t, paneText(h)[len(paneText(h))-1], "enter send · alt+enter interrupt")
	h.typeBytes("x")
	assert.NotContains(t, paneText(h)[len(paneText(h))-1], "enter send")
}

func TestSplit_PasteDuringArmingIsDiscardedAndRestartsIt(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, reply: reply})
	require.NoError(t, <-reply)
	h.advance(500 * time.Millisecond)
	h.send(tea.PasteMsg{Content: "y\r"})
	assert.Equal(t, 1, h.s.armDiscarded)
	h.advance(500 * time.Millisecond)
	assert.True(t, h.s.arming, "the paste restarted the window")
	h.advance(250 * time.Millisecond)
	assert.False(t, h.s.arming)
	h.send(tea.PasteMsg{Content: "z"})
	assert.Equal(t, 1, h.s.armDiscarded, "armed, a paste is the view's")
	assert.Empty(t, h.settleEngine(t))
}

// The queue's one pump keeps running while the view is closed: each event
// re-arms it, and a closed subscription ends it.
func TestSplit_QueuePumpRunsWhileTheViewIsClosed(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 30)
	ch := make(chan coord.QueueEvent, 2)
	h.s.apprEvents = ch
	ch <- coord.QueueEvent{Kind: coord.QueueAdded, ID: "a1"}
	cmd := h.s.applyApprovalsMsg(queueEventMsg{ok: true})
	require.NotNil(t, cmd, "an event while closed re-arms the pump")
	assert.Equal(t, queueEventMsg{ev: coord.QueueEvent{Kind: coord.QueueAdded, ID: "a1"}, ok: true}, cmd())
	assert.Nil(t, h.s.applyApprovalsMsg(queueEventMsg{ok: false}), "a closed subscription ends it")
	assert.Nil(t, h.s.applyApprovalsMsg(approvalsTickMsg{}), "a tick for a closed view is dropped")
}

func TestSplit_MouseClicksAskForCellMotion(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.send(paneEventsMsg{paneMouseMsg(1)})
	assert.Equal(t, tea.MouseModeCellMotion, h.s.View().MouseMode)
}
