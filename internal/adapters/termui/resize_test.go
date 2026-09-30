package termui

import (
	"bytes"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recvSize(t *testing.T, ch <-chan *agent.WindowSize) *agent.WindowSize {
	t.Helper()
	select {
	case ws, ok := <-ch:
		require.True(t, ok, "size channel closed unexpectedly")
		return ws
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a size event")
		return nil
	}
}

func TestResizeTranslator_ReservesRows_InitialAndSigwinch(t *testing.T) {
	src := make(chan *agent.WindowSize, 4)
	var sizes [][2]int
	rt := newResizeTranslator(src, 1, func(rows, cols int) { sizes = append(sizes, [2]int{rows, cols}) }, realClock{})

	src <- &agent.WindowSize{Rows: 24, Cols: 80} // watchResize's initial emit
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint16(23), ws.Rows, "engine PTY is rows−N")
	assert.Equal(t, uint16(80), ws.Cols)

	src <- &agent.WindowSize{Rows: 40, Cols: 120} // SIGWINCH
	ws = recvSize(t, rt.Out())
	assert.Equal(t, uint16(39), ws.Rows)
	assert.Equal(t, uint16(120), ws.Cols)

	assert.Equal(t, [][2]int{{24, 80}, {40, 120}}, sizes,
		"the surround sees every REAL size, translation only touches the engine")

	rows, cols := rt.Current()
	assert.Equal(t, 40, rows)
	assert.Equal(t, 120, cols)
	close(src)
}

// TestResizeTranslator_EstablishesSurroundRegionSynchronously pins the other
// half: run.go's watchResize emits the real terminal
// size synchronously (buffered) BEFORE termui.New/newResizeTranslator is ever
// called, and setupTerminalUI runs strictly before the engine is started — so
// if newResizeTranslator
// establishes the surround's scroll region synchronously too, the region is
// guaranteed to exist on the tty before the engine can possibly exist to
// paint into it. If establishment is left entirely to the async t.run
// goroutine's own scheduling instead, there is no such guarantee: the engine
// can win the race and its first frame scrolls the whole (unprotected)
// screen, shoving the surround bar onto "a new line" instead of the reserved
// bottom row. This test allows NO scheduling opportunity (no sleep, no
// channel receive) between construction and the assertion, so it only passes
// if the establishment happened on the caller's own goroutine.
func TestResizeTranslator_EstablishesSurroundRegionSynchronously(t *testing.T) {
	var tty bytes.Buffer
	sur := newTestSurround(&tty, BarInfo{Harp: "h"})

	src := make(chan *agent.WindowSize, 1)
	src <- &agent.WindowSize{Rows: 24, Cols: 80} // watchResize's pre-buffered initial emit

	rt := newResizeTranslator(src, sur.reserve, sur.SetSize, realClock{})

	assert.Contains(t, tty.String(), "\x1b[1;23r",
		"DECSTBM must already be on the tty by the time newResizeTranslator returns")

	select {
	case ws := <-rt.Out():
		assert.Equal(t, uint16(23), ws.Rows, "the translated engine size must also be queued synchronously")
	default:
		t.Fatal("translated initial size was not queued synchronously on Out()")
	}
	close(src)
}

func TestResizeTranslator_TinyTerminalForwardsUntranslated(t *testing.T) {
	src := make(chan *agent.WindowSize, 1)
	rt := newResizeTranslator(src, 1, nil, realClock{})
	src <- &agent.WindowSize{Rows: 5, Cols: 80} // below minRowsForReserve
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint16(5), ws.Rows, "no reservation below the threshold — matches the surround's predicate")
	close(src)
}

func TestResizeTranslator_ZeroReserveIsIdentity(t *testing.T) {
	src := make(chan *agent.WindowSize, 1)
	rt := newResizeTranslator(src, 0, nil, realClock{})
	src <- &agent.WindowSize{Rows: 24, Cols: 80}
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint16(24), ws.Rows)
	close(src)
}

func TestResizeTranslator_NudgeWigglesWithinEngineViewport(t *testing.T) {
	src := make(chan *agent.WindowSize, 1)
	clk := newFakeClock()
	rt := newResizeTranslator(src, 1, nil, clk)
	src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain the initial translated size

	rt.Nudge()
	first := recvSize(t, rt.Out())
	clk.Advance(nudgeWiggleSeparation)
	second := recvSize(t, rt.Out())
	assert.Equal(t, uint16(22), first.Rows,
		"wiggle one row SMALLER than the engine viewport (same-size TIOCSWINSZ raises no SIGWINCH)")
	assert.Equal(t, uint16(23), second.Rows, "then settle on the true engine size")
	close(src)
}

// TestResizeTranslator_NudgeSeparatesWiggleSteps pins that SIGWINCH is a
// non-queued signal, so the wiggle's two TIOCSWINSZ ioctls — the shrink then
// the restore — must not land back-to-back, or they can coalesce into one
// delivery: the child's handler runs once, reads only the FINAL (unchanged)
// size, and skips the repaint (claude's bottom input bar, in the live
// incident). The restore must wait out nudgeWiggleSeparation on the clock.
func TestResizeTranslator_NudgeSeparatesWiggleSteps(t *testing.T) {
	clk := newFakeClock()
	src := make(chan *agent.WindowSize, 1)
	rt := newResizeTranslator(src, 1, nil, clk)
	src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain the initial translated size

	rt.Nudge()
	assert.Equal(t, uint16(22), recvSize(t, rt.Out()).Rows, "the shrink goes at once")
	clk.Advance(nudgeWiggleSeparation - time.Millisecond)
	assert.Empty(t, rt.Out(), "the restore must not land before the separation has elapsed")
	clk.Advance(time.Millisecond)
	assert.Equal(t, uint16(23), recvSize(t, rt.Out()).Rows, "then the true engine size")
	close(src)
}

// TestResizeTranslator_NudgeRestoreUsesCurrentSizeNotStale pins that the
// deferred restore re-reads the size: a genuine resize landing inside the
// wiggle window must not be overtaken (same FIFO Out() channel) by the size
// captured at Nudge-call time, which would leave the child pty sized to a
// value the terminal no longer has until the next SIGWINCH.
func TestResizeTranslator_NudgeRestoreUsesCurrentSizeNotStale(t *testing.T) {
	clk := newFakeClock()
	src := make(chan *agent.WindowSize, 4)
	rt := newResizeTranslator(src, 1, nil, clk)
	src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain initial translated size (23)

	rt.Nudge()
	assert.Equal(t, uint16(22), recvSize(t, rt.Out()).Rows, "wiggle shrink")

	src <- &agent.WindowSize{Rows: 30, Cols: 80} // a real resize inside the window
	assert.Equal(t, uint16(29), recvSize(t, rt.Out()).Rows, "the real resize's translated size")

	clk.Advance(nudgeWiggleSeparation)
	assert.Equal(t, uint16(29), recvSize(t, rt.Out()).Rows,
		"the deferred restore sends the CURRENT effective size, never the one captured at Nudge-call time")
	close(src)
}

// TestResizeTranslator_NudgeWigglesEvenAtMinimalHeight pins that Nudge's
// eff.Rows<=1 branch used to send the CURRENT size unchanged, which raises no
// SIGWINCH (the kernel only signals on a real change) — exactly the failure
// Nudge and nudgeWiggleSeparation exist to fix. On a minimal-height drawable
// (eff.Rows==1) the post-overlay repaint never happened. Nudge must still
// produce a genuine transition even here (wiggling upward instead).
func TestResizeTranslator_NudgeWigglesEvenAtMinimalHeight(t *testing.T) {
	clk := newFakeClock()
	src := make(chan *agent.WindowSize, 1)
	rt := newResizeTranslator(src, 5, nil, clk)
	src <- &agent.WindowSize{Rows: 6, Cols: 80}
	first := recvSize(t, rt.Out())
	require.Equal(t, uint16(1), first.Rows, "test setup: eff.Rows must be exactly 1")

	rt.Nudge()
	wiggle := recvSize(t, rt.Out())
	clk.Advance(nudgeWiggleSeparation)
	restore := recvSize(t, rt.Out())
	assert.NotEqual(t, wiggle.Rows, restore.Rows,
		"the wiggle step must genuinely differ from the restore so the child's SIGWINCH handler observes a change")
	assert.Equal(t, uint16(1), restore.Rows, "settles back on the true effective size")
	close(src)
}

func TestResizeTranslator_NudgeBeforeAnySizeIsNoop(t *testing.T) {
	src := make(chan *agent.WindowSize)
	rt := newResizeTranslator(src, 1, nil, realClock{})
	rt.Nudge()
	select {
	case ws := <-rt.Out():
		t.Fatalf("unexpected size event %v before any real size", ws)
	case <-time.After(50 * time.Millisecond):
	}
	close(src)
}

func TestResizeTranslator_OutClosesWithSource(t *testing.T) {
	src := make(chan *agent.WindowSize)
	rt := newResizeTranslator(src, 1, nil, realClock{})
	close(src) // watchResize closes on ctx done
	select {
	case _, ok := <-rt.Out():
		assert.False(t, ok, "out must close so the client's resize pump ends cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("out did not close after src closed")
	}
}
