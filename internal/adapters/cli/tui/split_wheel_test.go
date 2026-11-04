package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
)

// An engine that asks for no mouse reports still sits on the split view's
// alternate screen. A terminal that reports nothing there turns the wheel
// into arrow keys (xterm.js always does, VTE by default), and the engine
// reads them as its prompt history. So the split view keeps the wheel
// reported over the engine's pane, whatever the engine asked for.
func TestSplitWheel_AQuietEngineStillHasTheWheelReported(t *testing.T) {
	h := scrolled(t, newSplitPort())
	require.Equal(t, termemu.MouseOff, h.s.mouseMode)
	assert.Equal(t, tea.MouseModeCellMotion, h.s.View().MouseMode)
}

// Over an engine that asked for no mouse, the wheel scrolls its history as
// tmux's copy-mode -e does: up enters scroll mode, and down back to the
// bottom leaves it. None of it reaches the engine as keys or reports.
func TestSplitWheel_WheelOverAQuietEngineScrollsItsHistory(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1b[<64;40;3M")
	require.True(t, h.s.scroll.on, "wheel up enters scroll mode")
	row, marker := first(h)
	assert.Equal(t, "L12", row)
	assert.Equal(t, "[3/14]", marker)
	h.typeBytes("\x1b[<64;40;3M")
	_, marker = first(h)
	assert.Equal(t, "[6/14]", marker)
	h.typeBytes("\x1b[<65;40;3M")
	_, marker = first(h)
	assert.Equal(t, "[3/14]", marker)
	assert.True(t, h.s.scroll.on)
	h.typeBytes("\x1b[<65;40;3M")
	assert.False(t, h.s.scroll.on, "down to the bottom is live again")
	assert.Equal(t, h.pane.Snapshot().Text(), paneText(h))
	assert.Empty(t, h.settleEngine(t), "the wheel was the split view's")
}

// The wheel's scroll ends at the bottom even when a notch overshoots it.
func TestSplitWheel_AnOvershootingNotchStillEndsAtTheBottom(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1b[<64;40;3M")
	h.keys("down")
	h.typeBytes("\x1b[<65;40;3M")
	assert.False(t, h.s.scroll.on)
}

// Scroll mode entered from the keyboard is left from the keyboard: the
// wheel reaching the bottom keeps it on.
func TestSplitWheel_KeyboardScrollIsNotLeftByTheWheel(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1d[")
	h.typeBytes("\x1b[<64;40;3M\x1b[<65;40;3M\x1b[<65;40;3M")
	_, marker := first(h)
	assert.Equal(t, "[0/14]", marker)
	assert.True(t, h.s.scroll.on)
}

// Live at the bottom already, wheel down over a quiet engine is nothing.
func TestSplitWheel_WheelDownWhileLiveIsNothing(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1b[<65;40;3M")
	assert.False(t, h.s.scroll.on)
	assert.Empty(t, h.settleEngine(t))
}

// With no history there is nothing to scroll back to: the wheel is nothing.
func TestSplitWheel_NoHistoryNoScroll(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 6)
	h.engineWrites("L01")
	require.Zero(t, h.s.frame.History)
	h.typeBytes("\x1b[<64;40;3M")
	assert.False(t, h.s.scroll.on)
	assert.Empty(t, h.settleEngine(t))
}

// Clicks, drags and motion over a quiet engine reach nothing: the engine
// never asked for them.
func TestSplitWheel_OtherMouseOverAQuietEngineReachesNothing(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1b[<0;40;3M\x1b[<32;41;3M\x1b[<0;41;3m\x1b[<35;42;4M")
	assert.False(t, h.s.scroll.on)
	assert.Empty(t, h.settleEngine(t))
}

// The wheel over the panel or the divider is not the pane's.
func TestSplitWheel_WheelOverThePanelIsNotThePanes(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.typeBytes("\x1b[<64;3;3M\x1b[<64;27;3M")
	assert.False(t, h.s.scroll.on)
	assert.Empty(t, h.settleEngine(t))
}

// An engine that asks for the mouse gets the wheel, as before.
func TestSplitWheel_AnEngineThatAsksGetsTheWheel(t *testing.T) {
	h := scrolled(t, newSplitPort())
	h.engineWrites("\x1b[?1000h\x1b[?1006h")
	require.Equal(t, termemu.MouseClicks, h.s.mouseMode)
	h.typeBytes("\x1b[<64;40;3M")
	assert.False(t, h.s.scroll.on)
	assert.Contains(t, h.settleEngine(t), "\x1b[<64;")
}

// Through the real program, as the human's terminal sees it: a quiet engine
// leaves the terminal reporting cell motion, and a wheel notch scrolls the
// history without a byte reaching the engine.
func TestSplitView_TheWheelScrollsAQuietEnginesHistory(t *testing.T) {
	p := startProgram(t, newSplitPort(), 100, 6)
	_, _ = p.v.Pane().Write([]byte(numbered(1, 20)))
	p.waitPaneShown(t)
	p.out.waitUntil(t, "cell-motion reporting", func(out []byte) bool { return mouseOf(out) == termemu.MouseDrags })
	_, _ = p.in.Write([]byte("\x1b[<64;40;3M"))
	p.out.waitUntil(t, "the wheel's scroll", func(out []byte) bool {
		return strings.Contains(strings.Join(screenOf(out, p.w, p.h).Text(), "\n"), "[3/14]")
	})
	_, _ = p.v.Pane().Write([]byte("\x1b[c"))
	p.engine.waitForMatch(t, daReply)
	assert.Empty(t, daReply.ReplaceAllString(p.engine.String(), ""), "no arrow keys, no reports")
	p.v.Close()
	assert.Equal(t, termemu.MouseOff, mouseOf(p.out.bytes()), "closed: the human's terminal reports nothing")
}

// The engine's mouse mode reaches the split view's renderer through the
// mailbox, after the pane has it. A wheel arriving in between follows the
// pane, which is what encodes it: the engine asked, so the engine gets it.
func TestSplitWheel_TheWheelFollowsThePaneNotTheMailbox(t *testing.T) {
	h := scrolled(t, newSplitPort())
	_, _ = h.pane.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	h.paneEvents = nil // not yet delivered
	require.Equal(t, termemu.MouseOff, h.s.mouseMode)
	h.typeBytes("\x1b[<64;40;3M")
	assert.False(t, h.s.scroll.on)
	assert.Contains(t, h.settleEngine(t), "\x1b[<64;")
}
