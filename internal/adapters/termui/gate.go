package termui

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// outputGate sits between the plugin client's output consumer and the tty:
// open, it writes through (and lets the surround flush a pending repaint on
// the writer's coattails — see surround); held, engine bytes land in a
// bounded hold buffer instead of the screen, and the engine is never made to
// wait (an unread pty would stall it). Release restores the screen behind a
// caller-supplied sequence, atomically with the held→open flip so no
// concurrent engine write can jump the restore.
type outputGate struct {
	mu   *sync.Mutex // the shared tty lock (surround paints under the same one)
	dst  io.Writer
	held bool

	// hold is the engine output held while an overlay is up, grown as it
	// arrives up to holdCap. Past holdCap the whole hold is dropped
	// (overflowed): a replay has to start where the engine's stream started,
	// and a drop-oldest tail starts wherever eviction left it — mid-sequence
	// or mid-rune — which the terminal then prints as text.
	hold       []byte
	holdCap    int
	overflowed bool

	// guard filters every child byte bound for the tty: it clamps/repairs
	// scroll-region clobbers and holds back trailing partial escape
	// sequences/runes so the written stream always ends at a boundary a bar
	// repaint may follow (see vtGuard). nil = raw passthrough.
	guard *vtGuard

	// afterWrite runs with mu held after each passthrough write — the
	// surround's dirty-flush piggyback, so bar repaints ride between engine
	// chunks; the guard guarantees those boundaries never split an escape
	// sequence or a rune.
	afterWrite func()

	lastWrite atomic.Int64 // unix nanos of the last passthrough write
}

// holdMode is how Release gives the screen back.
type holdMode int

const (
	// holdReplay returns to the exact screen the held bytes were produced
	// against and replays them in order.
	holdReplay holdMode = iota
	// holdDiscardRedraw drops the held bytes and clears the screen for the
	// engine to repaint (the release nudge makes it).
	holdDiscardRedraw
)

// restore is what the caller writes around a release, one sequence per mode.
type restore struct {
	replay []byte // holdReplay: before the held bytes
	clear  []byte // holdDiscardRedraw: first; leaves the cursor home on a blank screen
	notice []byte // holdDiscardRedraw after an overflow: said on the cleared screen's first row
	resume []byte // holdDiscardRedraw: after the notice (region, bar, engine cursor)
}

// newOutputGate wraps dst. mu is the tty lock shared with the surround;
// guard and afterWrite may be nil.
func newOutputGate(mu *sync.Mutex, dst io.Writer, guard *vtGuard, afterWrite func()) *outputGate {
	return &outputGate{mu: mu, dst: dst, guard: guard, afterWrite: afterWrite}
}

// Write implements the engine-output path.
func (g *outputGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		g.holdLocked(p)
		return len(p), nil
	}
	out := p
	if g.guard != nil {
		out = g.guard.Filter(p)
	}
	var err error
	if len(out) > 0 {
		_, err = g.dst.Write(out)
	}
	g.lastWrite.Store(nowNanos())
	if g.afterWrite != nil {
		g.afterWrite()
	}
	// Report the full chunk consumed: bytes the guard held back are pending
	// inside it, not lost.
	return len(p), err
}

func (g *outputGate) holdLocked(p []byte) {
	if g.overflowed {
		return
	}
	if len(g.hold)+len(p) > g.holdCap {
		g.overflowed = true
		g.hold = nil
		return
	}
	g.hold = append(g.hold, p...)
}

// Hold diverts engine output into a hold of at most capacity bytes (viewer
// engaged). Idempotent while held.
func (g *outputGate) Hold(capacity int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		return
	}
	g.held, g.holdCap, g.overflowed, g.hold = true, capacity, false, nil
}

// Release reopens the gate under the tty lock and reports the mode it used:
// mode as asked, except that an overflowed hold is always redrawn — there is
// nothing whole left to replay. An open gate (never held) just writes
// r.replay: nothing guarantees a caller never hits that path, and a terminal
// left unrestored with no diagnostic is worse than one extra write.
//
// Returns every write failure, joined: the held bytes exist nowhere else once
// Release returns, so a failing tty must not lose them silently. Callers
// surface a non-nil error (Controller's Close/release do, via Options.Warn).
func (g *outputGate) Release(mode holdMode, r restore) (holdMode, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var w errWriter
	w.dst = g.dst
	if !g.held {
		w.write(r.replay, "restore sequence")
		mode = holdReplay
	} else {
		mode = g.releaseHeldLocked(&w, mode, r)
	}
	// Run the bar-flush hook exactly as Write does, so a bar marked dirty by
	// the replay itself (the guard's Filter can call barDamaged) is repainted
	// on this same write cycle instead of staying blank until the engine's
	// next write — which, for an idle engine, may be never.
	if g.afterWrite != nil {
		g.afterWrite()
	}
	g.lastWrite.Store(nowNanos())
	return mode, w.err()
}

func (g *outputGate) releaseHeldLocked(w *errWriter, mode holdMode, r restore) holdMode {
	data, overflowed := g.hold, g.overflowed
	g.held, g.hold, g.overflowed = false, nil, false
	if overflowed {
		mode = holdDiscardRedraw
	}
	if mode == holdDiscardRedraw {
		if g.guard != nil {
			g.guard.abandonPending()
		}
		w.write(r.clear, "clear sequence")
		if overflowed {
			w.write(r.notice, "overflow notice")
		}
		w.write(r.resume, "resume sequence")
		return mode
	}
	w.write(r.replay, "restore sequence")
	if g.guard != nil && len(data) > 0 {
		// The replay is child bytes like any other: same clamping, same
		// holdback, so a region clobber recorded while the viewer was open
		// can't slip through on release.
		data = g.guard.Filter(data)
	}
	w.write(data, "held engine output")
	return mode
}

// errWriter writes every piece it is given, even after one fails, and keeps
// each failure.
type errWriter struct {
	dst  io.Writer
	errs []error
}

func (w *errWriter) write(p []byte, what string) {
	if len(p) == 0 {
		return
	}
	if _, err := w.dst.Write(p); err != nil {
		w.errs = append(w.errs, fmt.Errorf("writing %s (%d bytes): %w", what, len(p), err))
	}
}

func (w *errWriter) err() error { return errors.Join(w.errs...) }

// LastWriteNanos reports when the last passthrough write hit the tty — the
// surround's engine-idle heuristic.
func (g *outputGate) LastWriteNanos() int64 { return g.lastWrite.Load() }

// FlushGuard writes any bytes still held back inside the guard (a pending
// incomplete escape/CSI sequence or a split UTF-8 rune tail) straight to dst,
// under the tty lock. This is a TEARDOWN-ONLY operation: call it
// only from Controller.Close, after the final Release, when the engine is
// not going to write again. Calling it from an ordinary engagement release
// (the engine keeps running afterward) would tear a sequence the engine's
// own next write was going to complete — Release deliberately leaves that
// state alone for exactly that reason.
func (g *outputGate) FlushGuard() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.guard == nil {
		return nil
	}
	flushed := g.guard.Flush()
	if len(flushed) == 0 {
		return nil
	}
	_, err := g.dst.Write(flushed)
	return err
}
