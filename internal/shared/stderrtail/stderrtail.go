// Package stderrtail keeps a bounded, concurrency-safe TAIL of a child
// process's stderr, so a process that dies can still say WHY.
//
// It is the one implementation of a pattern this repo had already grown
// twice, independently, for the same reason: internal/lm/grpc's host runner
// and internal/lm/isolation's docker-direct runner each kept their own
// private ring so a runner dying pre-dial-home surfaced its stderr instead of
// a bare "exit status 1". The 2026-07-24 containerized-agent incident showed
// the pattern was missing at the layer where it mattered MOST — the engine
// subprocess itself (internal/acp spawned the ACP adapter with stderr
// INHERITED), so the adapter's dying words ("SyntaxError: Unexpected token
// 'with'", a Node 18 that could not load claude-code-acp's entry module) went
// to the container's stdout and nowhere else. The container is force-removed
// on teardown, so `docker logs` was too late: the evidence was unrecoverable
// in production and the diagnosis took 49 minutes.
//
// Two properties are load-bearing and neither is negotiable:
//
//   - STREAMED, not polled. The tail is filled by the child's own stderr pump
//     as bytes arrive, so it survives the child (and its container) being
//     destroyed. Anything that reads the output AFTER the fact — `docker
//     logs`, a log file inside a --rm container — is a diagnostic that
//     vanishes exactly when it is needed.
//   - BOUNDED. This text gets forwarded into transcripts and mailboxes;
//     unbounded engine stderr there is its own problem. A tail (the LAST
//     bytes) is the right shape, because a dying process says why at the end.
package stderrtail

import (
	"io"
	"os"
	"strings"
	"sync"
)

// DefaultBytes is the standard tail budget: enough to carry a panic trace, a
// module-loader error, or a config dump's fatal line; small enough that
// forwarding it into a transcript or a mailbox message is never a problem.
const DefaultBytes = 8192

// Ring is an io.Writer that retains only the last max bytes written to it.
// Concurrency-safe: the child's stderr pump writes while a reaper reads Tail.
type Ring struct {
	mu  sync.Mutex
	buf []byte
	max int
}

// New builds a Ring bounded to max bytes (DefaultBytes when max <= 0).
func New(max int) *Ring {
	if max <= 0 {
		max = DefaultBytes
	}
	return &Ring{max: max}
}

// Write appends p, discarding whatever falls off the front of the budget. It
// never errors and never short-writes: a diagnostic sink that could fail
// would put back-pressure on the child's stderr, which is the opposite of
// what this is for.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

// Tail returns the retained bytes, trimmed. Empty when the child said nothing.
func (r *Ring) Tail() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}

// TeeStderr returns a Ring plus the writer to hand a child's Cmd.Stderr: the
// ring AND the host's own stderr, so capturing a diagnostic never silences
// the passthrough a caller may already depend on (an operator watching a
// verbose run, a container whose stdout is its only log). Capture is
// ADDITIVE — it must not take an existing diagnostic away.
func TeeStderr(max int) (*Ring, io.Writer) {
	r := New(max)
	return r, io.MultiWriter(os.Stderr, r)
}
