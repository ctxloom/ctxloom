package claude

import (
	"bytes"
	"sync"
)

// cursorShow and cursorHide are DECTCEM — the standard private mode 25 that
// shows and hides the caret. claude's TUI hides the caret for the duration of
// a modal and shows it again afterwards, which is what makes it usable as a
// "is a decision pending" marker.
//
// MEASURED, not assumed (_measure/promptprobe): a pty harness drove a real
// claude through four states and recorded both this mode and bracketed paste.
//
//	                                  ESC[?2004    cursor ESC[?25
//	composer, idle                       ON          VISIBLE
//	composer, whole streaming turn       ON          VISIBLE
//	trust modal (startup)                ON          HIDDEN
//	permission modal (mid-session)       ON          HIDDEN
//
// Two conclusions, and the second is the one that matters. ESC[?2004 is a DEAD
// END: it reads set in every state including both modals, so a guard keyed on
// it never fires — the only reset ever observed was process teardown, which is
// easy to mistake for a modal signal. ESC[?25 discriminates in all four, and
// the second row is the viability check: the caret stays VISIBLE through an
// entire streaming turn, so a guard keyed on it does not block ordinary wakes.
//
// Scope: claude, one version. This says nothing about any other engine, which
// is exactly why the capability is discovered by assertion rather than assumed.
var (
	cursorShow = []byte("\x1b[?25h")
	cursorHide = []byte("\x1b[?25l")
)

// cursorTailKeep is how many trailing bytes of one write are carried into the
// next scan. It is one less than the marker length, which is the most that can
// straddle a chunk boundary while still forming a complete sequence — and
// keeping exactly that many also guarantees the carried tail can never contain
// a WHOLE marker on its own, so nothing is counted twice.
const cursorTailKeep = 5 // len("\x1b[?25h") - 1

// inputGate tracks whether claude is showing a modal, by watching its caret.
// The zero value reports NOT accepting text, which is the fail-closed start
// the capability requires: until claude has actually said otherwise, a wake
// waits. claude's TUI repaints continuously and emits the marker on any
// redraw, so this resolves in the ordinary course of a session rather than
// needing a modal to open first.
type inputGate struct {
	mu        sync.Mutex
	tail      []byte
	accepting bool
}

// Observe implements agent.InputGate.
func (g *inputGate) Observe(p []byte) {
	if len(p) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	// Join the previous write's tail before scanning. The pty chunks by
	// buffer size, not by escape sequence, so a marker can arrive split
	// across two writes; scanning each write alone would miss it. A missed
	// HIDE misses in the OPEN direction — the injector would believe a modal
	// had closed and write into it — which is the one direction this must
	// never get wrong.
	buf := p
	if len(g.tail) > 0 {
		buf = make([]byte, 0, len(g.tail)+len(p))
		buf = append(buf, g.tail...)
		buf = append(buf, p...)
	}

	// LAST occurrence of each, because one write can carry several
	// transitions and only the final one describes the current state.
	show, hide := bytes.LastIndex(buf, cursorShow), bytes.LastIndex(buf, cursorHide)
	if show >= 0 || hide >= 0 {
		g.accepting = show > hide
	}

	if len(buf) > cursorTailKeep {
		buf = buf[len(buf)-cursorTailKeep:]
	}
	g.tail = append(g.tail[:0], buf...)
}

// AcceptingText implements agent.InputGate.
func (g *inputGate) AcceptingText() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.accepting
}

// Observe implements agent.InputGate on the backend itself, so the host finds
// the capability by asserting on the backend it already holds.
func (b *ClaudeCode) Observe(p []byte) { b.gate.Observe(p) }

// AcceptingText implements agent.InputGate. False until claude's caret has
// been seen, and false for as long as a modal is displayed.
func (b *ClaudeCode) AcceptingText() bool { return b.gate.AcceptingText() }
