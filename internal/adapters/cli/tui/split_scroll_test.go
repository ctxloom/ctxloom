package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// The scroll tests' engine draws numbered lines into a six-row pane: twenty
// of them leave L01..L14 in the history and L15..L20 on the screen.

// numbered is lines L<from>..L<to>, each but the last ending its row.
func numbered(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		if i > from {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "L%02d", i)
	}
	return b.String()
}

// scrolled is a split whose engine has drawn L01..L20 into a six-row pane.
func scrolled(t *testing.T, port *splitPort) *splitHarness {
	t.Helper()
	h := newSplitHarness(t, port).sized(100, 6)
	h.engineWrites(numbered(1, 20))
	require.Equal(t, 14, h.s.frame.History)
	return h
}

// shown is the pane's rows with the scroll marker cut off the first, and
// the marker.
func shown(h *splitHarness) (rows []string, marker string) {
	rows = paneText(h)
	if i := strings.LastIndex(rows[0], "["); i >= 0 {
		rows[0], marker = strings.TrimRight(rows[0][:i], " "), rows[0][i:]
	}
	return rows, marker
}

// first is the pane's first row, and the marker.
func first(h *splitHarness) (string, string) {
	rows, marker := shown(h)
	return rows[0], marker
}

func TestSplitScroll_PrefixBracketEntersAtTheBottom(t *testing.T) {
	h := scrolled(t, newSplitPort())
	rows, marker := shown(h)
	require.Equal(t, "", marker, "live: no marker")
	require.Equal(t, strings.Split(numbered(15, 20), "\r\n"), rows)

	h.typeBytes("\x1d[")
	require.True(t, h.s.scroll.on)
	assert.Equal(t, focusPane, h.s.focus, "the pane holds the keyboard again, for scrolling")
	rows, marker = shown(h)
	assert.Equal(t, strings.Split(numbered(15, 20), "\r\n"), rows, "entered at the bottom: the live screen")
	assert.Equal(t, "[0/14]", marker, "lines back of the history's length")
	assert.True(t, h.panelHas(splitScrollHints), "the panel says how to scroll and leave")
	assert.False(t, h.panelHas(h.s.prefix.Caret()+" agents"))
	assert.Nil(t, h.s.View().Cursor, "no cursor over history")
}

func TestSplitScroll_PrefixPgUpEntersAPageBack(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d\x1b[5~")
	require.True(t, h.s.scroll.on)
	rows, marker := shown(h)
	assert.Equal(t, strings.Split(numbered(9, 14), "\r\n"), rows)
	assert.Equal(t, "[6/14]", marker)
}

// The tree's own [ and PgUp enter it too: the chord is the prefix, which
// hands the tree the keyboard, then the tree's key.
func TestSplitScroll_EnteredOnlyOverTheEnginesTerminal(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	h := scrolled(t, port)
	h.typeBytes("\x1d")
	h.keys("j", "[")
	assert.False(t, h.s.scroll.on, "a child's feed has no history to scroll")
	h.keys("pgup")
	assert.False(t, h.s.scroll.on)
	assert.Equal(t, focusList, h.s.focus)
	h.keys("k", "[")
	assert.True(t, h.s.scroll.on)
}

func TestSplitScroll_EveryMoveKey(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	steps := []struct {
		key, first, marker string
	}{
		{"up", "L14", "[1/14]"},
		{"up", "L13", "[2/14]"},
		{"down", "L14", "[1/14]"},
		{"pgup", "L08", "[7/14]"},
		{"pgup", "L02", "[13/14]"},
		{"pgup", "L01", "[14/14]"}, // a page past the top stops at it
		{"up", "L01", "[14/14]"},
		{"pgdown", "L07", "[8/14]"},
		{"G", "L15", "[0/14]"},
		{"down", "L15", "[0/14]"}, // the bottom is the live screen: no further
		{"pgdown", "L15", "[0/14]"},
		{"g", "L01", "[14/14]"},
	}
	for _, st := range steps {
		h.keys(st.key)
		row, marker := first(h)
		assert.Equal(t, st.first, row, "after %s", st.key)
		assert.Equal(t, st.marker, marker, "after %s", st.key)
	}
	assert.True(t, h.s.scroll.on, "G is the bottom, still scrolling")
	rows, _ := shown(h)
	assert.Equal(t, strings.Split(numbered(1, 6), "\r\n"), rows)
}

// Mid-way the pane is history above the live screen's first rows.
func TestSplitScroll_HistoryRunsIntoTheLiveScreen(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	h.keys("up", "up")
	rows, _ := shown(h)
	assert.Equal(t, strings.Split(numbered(13, 18), "\r\n"), rows)
}

func TestSplitScroll_QAndEscReturnToLive(t *testing.T) {
	for _, k := range []string{"q", "esc"} {
		t.Run(k, func(t *testing.T) {
			h := scrolled(t, newSplitPort())
			h.typeBytes("\x1d[")
			h.keys("g", k)
			assert.False(t, h.s.scroll.on)
			assert.Equal(t, focusPane, h.s.focus)
			rows, marker := shown(h)
			assert.Equal(t, strings.Split(numbered(15, 20), "\r\n"), rows)
			assert.Empty(t, marker)
			assert.Empty(t, h.settleEngine(t), "the key that left was not the engine's")
			h.keys("q")
			assert.Equal(t, "q", h.settleEngine(t), "live again: keys are the engine's")
		})
	}
}

// While scrolling nothing the human types, pastes or clicks reaches the
// engine, and the engine's output keeps landing.
func TestSplitScroll_InputIsTheScrollsOutputStillLands(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.engineWrites("\x1b[?1000h\x1b[?1006h\x1b[?2004h")
	h.typeBytes("\x1d[")
	h.typeBytes("abc\r\x03")
	h.send(tea.PasteMsg{Content: "pasted"})
	h.typeBytes("\x1b[<0;40;3M\x1b[<0;40;3m\x1b[<64;40;3M")
	assert.Empty(t, h.settleEngine(t))
	assert.True(t, h.s.scroll.on)

	h.engineWrites("\r\n" + numbered(21, 22))
	assert.Equal(t, 16, h.s.frame.History, "the engine's output kept landing")
}

// The view is anchored, as tmux's copy mode is: output arriving below holds
// the same lines in the pane, and only the marker counts the growth.
func TestSplitScroll_AnchoredAsOutputArrives(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d\x1b[5~")
	before, _ := shown(h)
	h.engineWrites("\r\n" + numbered(21, 23))
	rows, marker := shown(h)
	assert.Equal(t, before, rows)
	assert.Equal(t, "[9/17]", marker)

	h.typeBytes("\x1d[") // the prefix leaves, [ re-enters: at the bottom
	_, marker = shown(h)
	assert.Equal(t, "[0/17]", marker)
	h.engineWrites("\r\n" + numbered(24, 25))
	row, marker := first(h)
	assert.Equal(t, "L18", row, "anchored at the bottom too: the live screen moves on below it")
	assert.Equal(t, "[2/19]", marker)
}

// History that shrinks under the view (the engine cleared it) clamps the
// view to what is left.
func TestSplitScroll_ClearedHistoryClampsTheView(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	h.keys("up")
	h.engineWrites("\x1b[3J\x1b[HL99")
	require.Zero(t, h.s.frame.History)
	rows, marker := shown(h)
	assert.Equal(t, h.pane.Snapshot().Text(), rows, "the live screen")
	assert.Equal(t, "L99", rows[0])
	assert.Equal(t, "[0/0]", marker)
}

func TestSplitScroll_WheelScrollsThePane(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	h.typeBytes("\x1b[<64;40;3M")
	row, marker := first(h)
	assert.Equal(t, "L12", row)
	assert.Equal(t, "[3/14]", marker)
	h.typeBytes("\x1b[<64;40;3M\x1b[<65;40;3M")
	_, marker = first(h)
	assert.Equal(t, "[3/14]", marker, "down undoes up")
	h.typeBytes("\x1b[<64;3;3M\x1b[<64;27;3M") // over the panel, and the divider
	_, marker = first(h)
	assert.Equal(t, "[3/14]", marker, "the wheel over the panel is not the pane's")
	h.typeBytes("\x1b[<64;28;3M") // the pane's first column
	_, marker = first(h)
	assert.Equal(t, "[6/14]", marker)
	h.typeBytes("\x1b[<65;28;3M")
	h.typeBytes(strings.Repeat("\x1b[<65;40;3M", 3))
	_, marker = first(h)
	assert.Equal(t, "[0/14]", marker, "the bottom stops the wheel")
	assert.True(t, h.s.scroll.on, "the wheel never leaves scroll mode")
}

// The human's terminal reports the mouse only while scrolling, so the wheel
// scrolls there; every way out hands it back to what the engine asked for.
func TestSplitScroll_MouseModeIsRestoredOnEveryExit(t *testing.T) {
	exits := []struct {
		name string
		exit func(h *splitHarness)
	}{
		{"q", func(h *splitHarness) { h.keys("q") }},
		{"esc", func(h *splitHarness) { h.keys("esc") }},
		{"focus to the tree", func(h *splitHarness) { h.typeBytes("\x1d") }},
		{"another agent selected", func(h *splitHarness) { h.typeBytes("\x1d"); h.keys("j") }},
	}
	engine := []struct {
		name  string
		asked termemu.MouseTracking
		want  tea.MouseMode
	}{
		{"none", termemu.MouseOff, tea.MouseModeNone},
		{"all motion", termemu.MouseMotion, tea.MouseModeAllMotion},
	}
	for _, ex := range exits {
		for _, en := range engine {
			t.Run(ex.name+"/"+en.name, func(t *testing.T) {
				port := newSplitPort()
				port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
				h := scrolled(t, port)
				h.send(paneEventsMsg{paneMouseMsg(en.asked)})
				require.Equal(t, en.want, h.s.View().MouseMode)
				h.typeBytes("\x1d[")
				require.Equal(t, tea.MouseModeCellMotion, h.s.View().MouseMode, "scrolling: the wheel is reported")
				ex.exit(h)
				assert.False(t, h.s.scroll.on)
				if h.s.showingTerminal() {
					assert.Equal(t, en.want, h.s.View().MouseMode, "the engine's mode again")
				} else {
					assert.Equal(t, tea.MouseModeNone, h.s.View().MouseMode, "a child's feed reports no mouse")
				}
				if h.s.focus == focusPane {
					h.typeBytes("\x1d")
				}
				h.keys("k", "esc")
				require.True(t, h.s.showingTerminal())
				assert.False(t, h.s.scroll.on, "back on the engine, live, not scrolling")
				assert.Equal(t, en.want, h.s.View().MouseMode)
			})
		}
	}
}

// The engine changing its mouse mode mid-scroll is what the human's
// terminal gets on the way out.
func TestSplitScroll_TheEnginesLatestMouseModeIsTheOneRestored(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	h.send(paneEventsMsg{paneMouseMsg(termemu.MouseMotion)})
	assert.Equal(t, tea.MouseModeCellMotion, h.s.View().MouseMode)
	h.keys("q")
	assert.Equal(t, tea.MouseModeAllMotion, h.s.View().MouseMode)
}

// Scrolling is using the split view's own surface: a summon is refused with
// its notice, as it is while the tree has focus, rather than yanking the
// pane from under the reader.
func TestSplitScroll_ASummonWhileScrollingIsRefused(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := scrolled(t, port)
	h.typeBytes("\x1d[")
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, notice: "approval from brave-heron", reply: reply})
	require.ErrorIs(t, <-reply, ErrEngaged)
	assert.False(t, h.s.apprOpen)
	assert.True(t, h.s.scroll.on)
	assert.True(t, h.panelHas("approval from brave-heron"))
}

// Scroll state never outlives what it scrolls: whatever takes the pane or
// the keyboard away ends it, however it got there.
func TestSplitScroll_EndsWithTheEnginesPane(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := scrolled(t, port)
	h.typeBytes("\x1d[")
	h.s.apprOpen = true
	h.frame()
	assert.False(t, h.s.scroll.on, "the approvals view took the pane")
}

// mouseOf is the mouse reporting the human's terminal is left in by out,
// replayed on a reference terminal.
func mouseOf(out []byte) termemu.MouseTracking {
	var got termemu.MouseTracking
	ref := termemu.NewXVT(10, 2, io.Discard, termemu.Callbacks{Mouse: func(m termemu.MouseTracking) { got = m }})
	defer func() { _ = ref.Close() }()
	_, _ = ref.Write(out)
	return got
}

// Through the real program, as the human's terminal sees it: reporting
// switches to cell motion for scrolling, back to the engine's own on q, and
// off as the split view closes mid-scroll.
func TestSplitView_ScrollModeSwitchesTheHumansMouseReporting(t *testing.T) {
	p := startProgram(t, newSplitPort(), 100, 6)
	_, _ = p.v.Pane().Write([]byte("\x1b[?1003h\x1b[?1006h" + numbered(1, 20)))
	p.waitPaneShown(t)
	mouse := func(what string, want termemu.MouseTracking) {
		t.Helper()
		p.out.waitUntil(t, what, func(out []byte) bool { return mouseOf(out) == want })
	}
	screenHas := func(what, text string) {
		t.Helper()
		p.out.waitUntil(t, what, func(out []byte) bool {
			return strings.Contains(strings.Join(screenOf(out, p.w, p.h).Text(), "\n"), text)
		})
	}
	mouse("the engine's all-motion reporting", termemu.MouseMotion)

	_, _ = p.in.Write([]byte("\x1d["))
	mouse("cell-motion reporting for the wheel", termemu.MouseDrags)
	screenHas("the scroll marker", "[0/14]")
	_, _ = p.in.Write([]byte("\x1b[<64;40;3M"))
	screenHas("the wheel's scroll", "[3/14]")
	_, _ = p.v.Pane().Write([]byte("\x1b[c"))
	p.engine.waitForMatch(t, daReply)
	assert.Empty(t, daReply.ReplaceAllString(p.engine.String(), ""), "the wheel was the scroll's")

	_, _ = p.in.Write([]byte("q"))
	mouse("the engine's reporting again", termemu.MouseMotion)
	_, _ = p.in.Write([]byte("\x1d["))
	mouse("cell motion again", termemu.MouseDrags)
	p.v.Close()
	assert.Equal(t, termemu.MouseOff, mouseOf(p.out.bytes()), "closed mid-scroll: the human's terminal reports nothing")
}

// The marker overlays the first row's right end and keeps it exactly the
// pane's width, a wide glyph cut at its edge included; a pane too narrow
// for it goes unmarked.
func TestSplitScroll_TheMarkerKeepsTheRowsWidth(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(101, 6) // a 74-column pane: 69 left of the marker
	h.engineWrites(strings.Repeat("日", 36) + "\r\n" + numbered(1, 6))
	h.typeBytes("\x1d[")
	h.keys("g")
	row := h.s.render()[0]
	assert.Equal(t, 101, ansi.StringWidth(row))
	_, marker := first(h)
	assert.Equal(t, "[1/1]", marker)
	assert.Equal(t, strings.Repeat("日", 34)+" [1/1]", strings.SplitN(h.screen()[0], "│", 2)[1])

	h.send(tea.WindowSizeMsg{Width: 8, Height: 6})
	h.frame()
	_, pane := h.s.layout()
	require.Equal(t, 5, pane)
	for _, r := range h.s.render() {
		assert.Equal(t, 8, ansi.StringWidth(r))
	}
	_, marker = first(h)
	assert.Empty(t, marker, "no room for the marker")
}

// Entered before the split has a size, scroll mode renders nothing and
// survives until it has one.
func TestSplitScroll_BeforeTheFirstSize(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.typeBytes("\x1d[")
	require.True(t, h.s.scroll.on)
	assert.Empty(t, h.s.render())
	h.sized(100, 6)
	_, marker := first(h)
	assert.Equal(t, "[0/0]", marker)
}

// The history can lose lines the held frame still counts: cleared while a
// synchronized frame is open. Rows for lines no longer there are blank
// until the frame commits, never another line.
func TestSplitScroll_LinesGoneUnderAHeldFrameAreBlank(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	h.keys("up")
	h.engineWrites("\x1b[?2026h\x1b[3J")
	require.Equal(t, 14, h.s.frame.History, "the held frame")
	rows, marker := shown(h)
	assert.Equal(t, append([]string{""}, strings.Split(numbered(15, 19), "\r\n")...), rows)
	assert.Equal(t, "[1/14]", marker)
}
