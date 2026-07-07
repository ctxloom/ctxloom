package termui

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// OutputGate sits between the plugin client's output consumer and the tty:
// open, it writes through (and lets the surround flush a pending repaint on
// the writer's coattails — see Surround); held, engine bytes land in the
// bounded ring instead of the screen. Release replays the held bytes behind a
// caller-supplied restore sequence, atomically with the held→open flip so no
// concurrent engine write can jump the replay.
type OutputGate struct {
	mu   *sync.Mutex // the shared tty lock (surround paints under the same one)
	dst  io.Writer
	ring *Ring
	held bool

	// afterWrite runs with mu held after each passthrough write — the
	// surround's dirty-flush piggyback, so bar repaints ride between engine
	// chunks instead of racing into the middle of a split escape sequence.
	afterWrite func()

	lastWrite atomic.Int64 // unix nanos of the last passthrough write
}

// NewOutputGate wraps dst. mu is the tty lock shared with the Surround;
// afterWrite may be nil.
func NewOutputGate(mu *sync.Mutex, dst io.Writer, holdCapacity int, afterWrite func()) *OutputGate {
	return &OutputGate{mu: mu, dst: dst, ring: NewRing(holdCapacity), afterWrite: afterWrite}
}

// Write implements the engine-output path.
func (g *OutputGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		return g.ring.Write(p)
	}
	n, err := g.dst.Write(p)
	g.lastWrite.Store(nowNanos())
	if g.afterWrite != nil {
		g.afterWrite()
	}
	return n, err
}

// Hold diverts engine output into the ring (viewer engaged). Idempotent.
func (g *OutputGate) Hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held = true
}

// Release reopens the gate: writes pre (the screen-restore sequence), replays
// the held bytes, and appends a truncation notice when the ring overflowed —
// all under the tty lock. A no-op when not held.
func (g *OutputGate) Release(pre []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return
	}
	g.held = false
	data, dropped := g.ring.Drain()
	if len(pre) > 0 {
		_, _ = g.dst.Write(pre)
	}
	if len(data) > 0 {
		_, _ = g.dst.Write(data)
	}
	if dropped > 0 {
		_, _ = fmt.Fprintf(g.dst, "\r\n\x1b[7m ctxloom: %d bytes of engine output dropped while the viewer was open \x1b[0m\r\n", dropped)
	}
	g.lastWrite.Store(nowNanos())
}

// LastWriteNanos reports when the last passthrough write hit the tty — the
// surround's engine-idle heuristic.
func (g *OutputGate) LastWriteNanos() int64 { return g.lastWrite.Load() }
