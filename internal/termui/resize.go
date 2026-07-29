package termui

import (
	"sync"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// resizeTranslator sits on the existing resize seam (run_resize_unix.go →
// client stdin pump → engine PTY): it forwards every size event — the initial
// emit and every SIGWINCH — with the surround's reserved rows subtracted, so
// the engine's PTY is allocated (rows−N) tall and can never address the bar
// row. It also notifies the surround of the REAL size (region + bar repaint)
// and provides the repaint nudge the controller fires after an overlay
// disengages.
type resizeTranslator struct {
	out     chan *pb.WindowSize
	reserve int
	onSize  func(rows, cols int) // surround SetSize (real size); may be nil

	mu         sync.Mutex
	rows, cols uint32 // last REAL size seen
	closed     bool   // out closed (src ended); guards Nudge racing the close
}

// newResizeTranslator starts forwarding from src (closing out when src
// closes, matching watchResize's contract so the client pump ends cleanly).
//
// If src already has an initial size buffered — watchResize (and its Windows
// twin) always emit one synchronously before returning the channel — it is
// drained and applied HERE, on the caller's own goroutine, not left to the
// async run loop below. This is DEFECT lucid-judo's ordering fix: the caller
// (setupTerminalUI, from run.go, well before the engine's Run stream is
// started) then returns to code that runs strictly before the engine can
// exist to produce output, so onSize (the surround's SetSize, which emits
// the DECSTBM scroll region) is GUARANTEED to have already reached the tty —
// not merely likely to have, pending a goroutine's own scheduling — before
// the engine's first paint could possibly land. Non-blocking: if nothing is
// buffered yet (GetSize failed, or a test source fed after construction),
// this is a no-op and the async path below carries it exactly as before.
func newResizeTranslator(src <-chan *pb.WindowSize, reserve int, onSize func(rows, cols int)) *resizeTranslator {
	t := &resizeTranslator{
		// Small buffer: Nudge sends a wiggle pair and must not block the
		// disengage path if the run stream already died.
		out:     make(chan *pb.WindowSize, 4),
		reserve: reserve,
		onSize:  onSize,
	}
	select {
	case ws, ok := <-src:
		if ok {
			t.consume(ws)
		}
	default:
	}
	go t.run(src)
	return t
}

// Out is the translated channel handed to the plugin client's Run.
func (t *resizeTranslator) Out() <-chan *pb.WindowSize { return t.out }

func (t *resizeTranslator) run(src <-chan *pb.WindowSize) {
	defer func() {
		// Close under the lock: Nudge (the overlay-disengage path) may race
		// the source ending, and a send on a closed channel would panic.
		t.mu.Lock()
		t.closed = true
		close(t.out)
		t.mu.Unlock()
	}()
	for ws := range src {
		t.consume(ws)
	}
}

// consume applies one REAL size event: record it, notify the surround, and
// forward the translated (reservation-subtracted) size to Out(). Shared by
// newResizeTranslator's synchronous initial drain and run's async loop so
// both paths behave identically.
func (t *resizeTranslator) consume(ws *pb.WindowSize) {
	t.mu.Lock()
	t.rows, t.cols = ws.Rows, ws.Cols
	t.mu.Unlock()
	if t.onSize != nil {
		t.onSize(int(ws.Rows), int(ws.Cols))
	}
	t.send(t.Translate(ws))
}

// Translate subtracts the reservation when it applies at this size (same
// predicate the surround uses, so viewport and protected region agree).
func (t *resizeTranslator) Translate(ws *pb.WindowSize) *pb.WindowSize {
	rows := ws.Rows
	if reserveActive(int(rows), t.reserve) {
		rows -= uint32(t.reserve)
	}
	return &pb.WindowSize{Rows: rows, Cols: ws.Cols}
}

// Current returns the last REAL terminal size (0,0 before the first event).
func (t *resizeTranslator) Current() (rows, cols int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int(t.rows), int(t.cols)
}

// nudgeWiggleSeparation is the pause between the wiggle's shrink and restore
// sizes (DEFECT racy-fling). SIGWINCH is a NON-QUEUED signal: each of the two
// sizes becomes one TIOCSWINSZ ioctl down at the pty (ptyrunner's apply loop),
// and if both land before the child's handler gets scheduled and reads
// TIOCGWINSZ, the two deliveries coalesce into one — the child then observes
// only the FINAL size, which equals what it already had, concludes nothing
// changed, and skips the very repaint Nudge exists to force (this is how
// closing the Ctrl-] overlay left claude's bottom input bar stale/corrupted).
// A real transition has to be OBSERVED, not just sent, so the fix is
// separation, not a different sequence of sizes. This isn't a wire-level
// acknowledgement (that would need a new Session method — a contract change),
// so it's a bounded wait long enough for the intermediate size to travel the
// send→gate hop chain out to the pty and for the child to receive and act on
// the resulting SIGWINCH — local IPC plus a signal handler, single-digit
// milliseconds in the worst realistic case. Nudge fires once per Ctrl-]
// overlay close (not a hot path), so a bounded sleep here is the pragmatic
// fix over reworking wire-level signaling.
const nudgeWiggleSeparation = 50 * time.Millisecond

// Nudge forces an engine repaint after an overlay disengages: a same-size
// TIOCSWINSZ raises no SIGWINCH (the kernel only signals on change), so it
// re-sends the engine size via a one-row wiggle — (rows−N−1) then (rows−N),
// genuinely separated in time (see nudgeWiggleSeparation) so the two don't
// coalesce into one delivery. The wiggle stays inside the engine's viewport
// so nothing ever addresses the reserved rows. At a minimal-height drawable
// (eff.Rows<=1, U141-F17) there is no room to shrink into, so the wiggle
// goes the other way — (rows−N+1) then (rows−N) — still a genuine
// transition rather than the same-size send that used to raise no SIGWINCH
// at all at this height.
func (t *resizeTranslator) Nudge() {
	t.mu.Lock()
	rows, cols := t.rows, t.cols
	t.mu.Unlock()
	if rows == 0 {
		return
	}
	eff := t.Translate(&pb.WindowSize{Rows: rows, Cols: cols})
	// U141-F17: a same-size TIOCSWINSZ raises no SIGWINCH, so the wiggle step
	// must always differ from eff — shrink by one normally, but at a
	// minimal-height drawable (eff.Rows<=1) there is no room to shrink into,
	// so wiggle upward instead. Either way this is a genuine transition the
	// child's handler will observe, unlike sending eff itself unchanged.
	wiggleRows := eff.Rows - 1
	if eff.Rows <= 1 {
		wiggleRows = eff.Rows + 1
	}
	t.send(&pb.WindowSize{Rows: wiggleRows, Cols: eff.Cols})
	// Off the caller's goroutine: Nudge runs inside Controller.release, under
	// the controller's sessionMu (held for the whole overlay teardown), and
	// a re-engage blocks on that same lock — a synchronous sleep here would
	// hold up the NEXT Ctrl-] press for nudgeWiggleSeparation for no reason.
	//
	// DEFECT racy-fling's restore half must NOT re-send the `eff` captured
	// above: that's the size at Nudge-call time, and a genuine SIGWINCH can
	// land in the nudgeWiggleSeparation window and update t.rows/t.cols
	// before this goroutine wakes. Sending the stale captured size then
	// would overtake (FIFO, same out channel) the real resize and leave the
	// child pty sized to a value the terminal no longer has, with nothing to
	// correct it until the next SIGWINCH. Re-read the CURRENT size under the
	// lock and re-translate it here instead: if nothing changed this is a
	// harmless duplicate of `eff`; if a real resize did land, this re-asserts
	// the CURRENT size rather than clobbering it.
	go func() {
		time.Sleep(nudgeWiggleSeparation)
		t.mu.Lock()
		rows, cols := t.rows, t.cols
		t.mu.Unlock()
		if rows == 0 {
			return
		}
		t.send(t.Translate(&pb.WindowSize{Rows: rows, Cols: cols}))
	}()
}

// send is latest-wins like watchResize: a full buffer means the consumer is
// gone or far behind — evict the stalest event rather than block.
func (t *resizeTranslator) send(ws *pb.WindowSize) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	select {
	case t.out <- ws:
		return
	default:
	}
	select {
	case <-t.out:
	default:
	}
	select {
	case t.out <- ws:
	default:
	}
}
