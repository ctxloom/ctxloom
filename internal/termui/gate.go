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
// the writer's coattails — see surround); held, engine bytes land in the
// bounded ring instead of the screen. Release replays the held bytes behind a
// caller-supplied restore sequence, atomically with the held→open flip so no
// concurrent engine write can jump the replay.
type outputGate struct {
	mu   *sync.Mutex // the shared tty lock (surround paints under the same one)
	dst  io.Writer
	ring *ring
	held bool

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

// newOutputGate wraps dst. mu is the tty lock shared with the surround;
// guard and afterWrite may be nil.
func newOutputGate(mu *sync.Mutex, dst io.Writer, holdCapacity int, guard *vtGuard, afterWrite func()) *outputGate {
	return &outputGate{mu: mu, dst: dst, ring: newRing(holdCapacity), guard: guard, afterWrite: afterWrite}
}

// Write implements the engine-output path.
func (g *outputGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		return g.ring.Write(p)
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

// Hold diverts engine output into the ring (viewer engaged). Idempotent.
func (g *outputGate) Hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held = true
}

// Release reopens the gate: writes pre (the screen-restore sequence), replays
// the held bytes, and appends a truncation notice when the ring overflowed —
// all under the tty lock. A no-op when not held.
//
// Returns the first write failure encountered, if any (U141-F01): a failing
// tty used to silently lose the entire replay — the ring is drained
// unconditionally above, so those bytes exist nowhere else once Release
// returns, and nothing signaled that they were gone. Callers should surface
// a non-nil error (Controller's Close/release do, via Options.Warn) rather
// than swallow it a second time.
func (g *outputGate) Release(pre []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return nil
	}
	g.held = false
	data, dropped := g.ring.Drain()
	var errs []error
	if len(pre) > 0 {
		// pre is the controller's own restore sequence — trusted, never filtered.
		if _, err := g.dst.Write(pre); err != nil {
			errs = append(errs, fmt.Errorf("writing restore sequence: %w", err))
		}
	}
	if g.guard != nil && len(data) > 0 {
		// The replay is child bytes like any other: same clamping, same
		// holdback, so a region clobber recorded while the viewer was open
		// can't slip through on release.
		data = g.guard.Filter(data)
	}
	if len(data) > 0 {
		if _, err := g.dst.Write(data); err != nil {
			errs = append(errs, fmt.Errorf("writing %d bytes of held engine output: %w", len(data), err))
		}
	}
	if dropped > 0 {
		if _, err := fmt.Fprintf(g.dst, "\r\n\x1b[7m ctxloom: %d bytes of engine output dropped while the viewer was open \x1b[0m\r\n", dropped); err != nil {
			errs = append(errs, fmt.Errorf("writing drop notice: %w", err))
		}
	}
	g.lastWrite.Store(nowNanos())
	return errors.Join(errs...)
}

// LastWriteNanos reports when the last passthrough write hit the tty — the
// surround's engine-idle heuristic.
func (g *outputGate) LastWriteNanos() int64 { return g.lastWrite.Load() }
