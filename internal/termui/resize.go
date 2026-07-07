package termui

import (
	"sync"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// ResizeTranslator sits on the existing resize seam (run_resize_unix.go →
// client stdin pump → engine PTY): it forwards every size event — the initial
// emit and every SIGWINCH — with the surround's reserved rows subtracted, so
// the engine's PTY is allocated (rows−N) tall and can never address the bar
// row. It also notifies the surround of the REAL size (region + bar repaint)
// and provides the repaint nudge the controller fires after an overlay
// disengages.
type ResizeTranslator struct {
	out     chan *pb.WindowSize
	reserve int
	onSize  func(rows, cols int) // surround SetSize (real size); may be nil

	mu         sync.Mutex
	rows, cols uint32 // last REAL size seen
	closed     bool   // out closed (src ended); guards Nudge racing the close
}

// NewResizeTranslator starts forwarding from src (closing out when src
// closes, matching watchResize's contract so the client pump ends cleanly).
func NewResizeTranslator(src <-chan *pb.WindowSize, reserve int, onSize func(rows, cols int)) *ResizeTranslator {
	t := &ResizeTranslator{
		// Small buffer: Nudge sends a wiggle pair and must not block the
		// disengage path if the run stream already died.
		out:     make(chan *pb.WindowSize, 4),
		reserve: reserve,
		onSize:  onSize,
	}
	go t.run(src)
	return t
}

// Out is the translated channel handed to the plugin client's Run.
func (t *ResizeTranslator) Out() <-chan *pb.WindowSize { return t.out }

func (t *ResizeTranslator) run(src <-chan *pb.WindowSize) {
	defer func() {
		// Close under the lock: Nudge (the overlay-disengage path) may race
		// the source ending, and a send on a closed channel would panic.
		t.mu.Lock()
		t.closed = true
		close(t.out)
		t.mu.Unlock()
	}()
	for ws := range src {
		t.mu.Lock()
		t.rows, t.cols = ws.Rows, ws.Cols
		t.mu.Unlock()
		if t.onSize != nil {
			t.onSize(int(ws.Rows), int(ws.Cols))
		}
		t.send(t.Translate(ws))
	}
}

// Translate subtracts the reservation when it applies at this size (same
// predicate the surround uses, so viewport and protected region agree).
func (t *ResizeTranslator) Translate(ws *pb.WindowSize) *pb.WindowSize {
	rows := ws.Rows
	if reserveActive(int(rows), t.reserve) {
		rows -= uint32(t.reserve)
	}
	return &pb.WindowSize{Rows: rows, Cols: ws.Cols}
}

// Current returns the last REAL terminal size (0,0 before the first event).
func (t *ResizeTranslator) Current() (rows, cols int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int(t.rows), int(t.cols)
}

// Nudge forces an engine repaint after an overlay disengages: a same-size
// TIOCSWINSZ raises no SIGWINCH (the kernel only signals on change), so it
// re-sends the engine size via a one-row wiggle — (rows−N−1) then (rows−N).
// The wiggle stays inside the engine's viewport so nothing ever addresses
// the reserved rows.
func (t *ResizeTranslator) Nudge() {
	t.mu.Lock()
	rows, cols := t.rows, t.cols
	t.mu.Unlock()
	if rows == 0 {
		return
	}
	eff := t.Translate(&pb.WindowSize{Rows: rows, Cols: cols})
	if eff.Rows > 1 {
		t.send(&pb.WindowSize{Rows: eff.Rows - 1, Cols: eff.Cols})
	}
	t.send(eff)
}

// send is latest-wins like watchResize: a full buffer means the consumer is
// gone or far behind — evict the stalest event rather than block.
func (t *ResizeTranslator) send(ws *pb.WindowSize) {
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
