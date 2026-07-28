package termui

import (
	"bytes"
	"testing"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recvSize(t *testing.T, ch <-chan *pb.WindowSize) *pb.WindowSize {
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
	src := make(chan *pb.WindowSize, 4)
	var sizes [][2]int
	rt := newResizeTranslator(src, 1, func(rows, cols int) { sizes = append(sizes, [2]int{rows, cols}) })

	src <- &pb.WindowSize{Rows: 24, Cols: 80} // watchResize's initial emit
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint32(23), ws.Rows, "engine PTY is rows−N")
	assert.Equal(t, uint32(80), ws.Cols)

	src <- &pb.WindowSize{Rows: 40, Cols: 120} // SIGWINCH
	ws = recvSize(t, rt.Out())
	assert.Equal(t, uint32(39), ws.Rows)
	assert.Equal(t, uint32(120), ws.Cols)

	assert.Equal(t, [][2]int{{24, 80}, {40, 120}}, sizes,
		"the surround sees every REAL size, translation only touches the engine")

	rows, cols := rt.Current()
	assert.Equal(t, 40, rows)
	assert.Equal(t, 120, cols)
	close(src)
}

// TestResizeTranslator_EstablishesSurroundRegionSynchronously pins the other
// half of DEFECT lucid-judo: run.go's watchResize emits the real terminal
// size synchronously (buffered) BEFORE termui.New/newResizeTranslator is ever
// called, and setupTerminalUI runs strictly before the plugin's Run stream is
// started (goplugin.Launcher.Start / RunStart) — so if newResizeTranslator
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

	src := make(chan *pb.WindowSize, 1)
	src <- &pb.WindowSize{Rows: 24, Cols: 80} // watchResize's pre-buffered initial emit

	rt := newResizeTranslator(src, sur.reserve, sur.SetSize)

	assert.Contains(t, tty.String(), "\x1b[1;23r",
		"DECSTBM must already be on the tty by the time newResizeTranslator returns")

	select {
	case ws := <-rt.Out():
		assert.Equal(t, uint32(23), ws.Rows, "the translated engine size must also be queued synchronously")
	default:
		t.Fatal("translated initial size was not queued synchronously on Out()")
	}
	close(src)
}

func TestResizeTranslator_TinyTerminalForwardsUntranslated(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := newResizeTranslator(src, 1, nil)
	src <- &pb.WindowSize{Rows: 5, Cols: 80} // below minRowsForReserve
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint32(5), ws.Rows, "no reservation below the threshold — matches the surround's predicate")
	close(src)
}

func TestResizeTranslator_ZeroReserveIsIdentity(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := newResizeTranslator(src, 0, nil)
	src <- &pb.WindowSize{Rows: 24, Cols: 80}
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint32(24), ws.Rows)
	close(src)
}

func TestResizeTranslator_NudgeWigglesWithinEngineViewport(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := newResizeTranslator(src, 1, nil)
	src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain the initial translated size

	rt.Nudge()
	first := recvSize(t, rt.Out())
	second := recvSize(t, rt.Out())
	assert.Equal(t, uint32(22), first.Rows,
		"wiggle one row SMALLER than the engine viewport (same-size TIOCSWINSZ raises no SIGWINCH)")
	assert.Equal(t, uint32(23), second.Rows, "then settle on the true engine size")
	close(src)
}

// TestResizeTranslator_NudgeSeparatesWiggleSteps pins DEFECT racy-fling: SIGWINCH
// is a non-queued signal, so the wiggle's two TIOCSWINSZ ioctls — the shrink
// then the restore — must not land back-to-back with no separation, or they
// can coalesce into a single delivery. The child's handler then runs once,
// reads TIOCGWINSZ, sees only the FINAL (unchanged) size, concludes nothing
// happened, and skips its repaint — corrupting whatever it had drawn where
// the overlay used to be (claude's bottom input bar, in the live incident).
// This asserts a genuine time gap between the two sizes landing on Out(),
// not just their values, since a same-size wiggle sent with zero separation
// degenerates back into exactly the bug Nudge exists to fix.
func TestResizeTranslator_NudgeSeparatesWiggleSteps(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := newResizeTranslator(src, 1, nil)
	src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain the initial translated size

	start := time.Now()
	rt.Nudge()
	first := recvSize(t, rt.Out())
	t1 := time.Since(start)
	second := recvSize(t, rt.Out())
	t2 := time.Since(start)

	assert.Equal(t, uint32(22), first.Rows)
	assert.Equal(t, uint32(23), second.Rows)
	assert.Greater(t, t2-t1, 10*time.Millisecond,
		"the wiggle's shrink and restore must be genuinely separated in time so the "+
			"child's non-queued SIGWINCH handler has a chance to observe the intermediate "+
			"size before the final one lands — sent back-to-back, the two can coalesce into "+
			"one delivery that reports no change")
	close(src)
}

// TestResizeTranslator_NudgeRestoreUsesCurrentSizeNotStale pins the T1 fix
// for DEFECT racy-fling's restore half: the deferred restore send must not
// re-assert the size captured at Nudge-call time if a genuine resize lands
// during the nudgeWiggleSeparation window. The stale send would overtake
// (same FIFO Out() channel) the real resize and leave the child pty sized to
// a value the terminal no longer has, until the next SIGWINCH. This injects
// a real resize partway through the wiggle window and asserts the LAST size
// delivered on Out() is the real resize's translated size, not the
// pre-Nudge value.
func TestResizeTranslator_NudgeRestoreUsesCurrentSizeNotStale(t *testing.T) {
	src := make(chan *pb.WindowSize, 4)
	rt := newResizeTranslator(src, 1, nil)
	src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain initial translated size (23)

	rt.Nudge() // schedules shrink now (22) + a restore at +nudgeWiggleSeparation
	first := recvSize(t, rt.Out())
	assert.Equal(t, uint32(22), first.Rows, "wiggle shrink")

	// Inject a real resize inside the wiggle window, well before the
	// restore goroutine wakes.
	time.Sleep(nudgeWiggleSeparation / 3)
	src <- &pb.WindowSize{Rows: 30, Cols: 80} // real SIGWINCH-driven resize
	real := recvSize(t, rt.Out())
	assert.Equal(t, uint32(29), real.Rows, "the real resize's translated size")

	// Wait past the restore's scheduled delivery. It must re-assert the
	// CURRENT effective size (29, a harmless duplicate), never the STALE
	// size captured when Nudge was called (23).
	restored := recvSize(t, rt.Out())
	assert.Equal(t, uint32(29), restored.Rows,
		"the deferred restore must send the CURRENT effective size, not the "+
			"value captured at Nudge-call time, or it clobbers a real resize "+
			"that landed during the wiggle window")

	close(src)
}

func TestResizeTranslator_NudgeBeforeAnySizeIsNoop(t *testing.T) {
	src := make(chan *pb.WindowSize)
	rt := newResizeTranslator(src, 1, nil)
	rt.Nudge()
	select {
	case ws := <-rt.Out():
		t.Fatalf("unexpected size event %v before any real size", ws)
	case <-time.After(50 * time.Millisecond):
	}
	close(src)
}

func TestResizeTranslator_OutClosesWithSource(t *testing.T) {
	src := make(chan *pb.WindowSize)
	rt := newResizeTranslator(src, 1, nil)
	close(src) // watchResize closes on ctx done
	select {
	case _, ok := <-rt.Out():
		assert.False(t, ok, "out must close so the client's resize pump ends cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("out did not close after src closed")
	}
}
