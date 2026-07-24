// Package vpio names the VIRTUALIZED-PROCESS-IO seam: the host-side
// transport for an interactive agent turn, formalized behind one interface
// so the frontend never touches a transport directly.
//
// The role split this package draws a line around:
//
//   - ABOVE the seam (this package's consumers, e.g. internal/cli/run.go):
//     raw-terminal ownership, SIGWINCH→Resize plumbing, TERM/COLORTERM env
//     forwarding (already spawn-time, in internal/lm/isolation — orthogonal
//     to this seam and unchanged by it), the termui surround/interceptor,
//     stdin-close semantics, exit-code propagation. Written once, against
//     Launcher/Session, never against a transport's own types.
//   - BELOW the seam (a Launcher/Session implementation): everything
//     transport-specific — how the interactive stream is actually carried.
//
// The current (only) implementation is internal/vpio/goplugin, which wraps
// the existing hashicorp/go-plugin-backed bidirectional Run RPC
// (internal/lm/grpc, llm.proto's `Run`) — a MOVE of the pump/relay logic
// that used to live inline at the call site, not a rewrite of the wire
// protocol. This package does not rename github.com/hashicorp/go-plugin (the
// upstream dependency) or the llm.proto Run RPC; it wraps them.
//
// Two more implementations are REGISTERED FUTURE WORK, not implemented
// here — the interface is shaped so they can plug in without any
// above-the-seam caller changing:
//
//   - docker-exec: attaches to an already-running container's process via
//     `docker exec -it`, for the container-isolation runtime.
//   - host-pty: spawns a bare local process on a host pty, for a
//     non-plugin engine.
package vpio

import (
	"context"
	"io"
	"os"
)

// ProcessSpec is the per-invocation interactive contract a Launcher starts:
// the stdio the frontend already owns. What to launch (the backend/session
// identity — e.g. the go-plugin implementation's already-connected client
// and its resolved RunStart) is bound into the Launcher at construction, not
// carried here — ProcessSpec carries only what varies per interactive turn.
type ProcessSpec struct {
	// Stdin is the frontend's keystroke source. nil for a non-interactive
	// (oneshot) turn — the Launcher must not block trying to read it.
	Stdin io.Reader
	// Stdout receives the session's output.
	Stdout io.Writer
	// Stderr receives the session's diagnostic output.
	Stderr io.Writer
}

// ExitStatus is the terminal result of a Session.
type ExitStatus struct {
	// Code is the session's exit code (0 = success).
	Code int32
}

// Session is a started interactive turn: the stdio wired at Start, plus the
// control primitives above-the-seam code drives it with.
type Session interface {
	// Resize notifies the session of a terminal size change (rows, cols).
	// Above-the-seam code calls this once per SIGWINCH-derived size event;
	// implementations must not block the caller on a slow or finished
	// transport (coalesce/drop rather than stall the resize pump).
	Resize(rows, cols uint32)

	// Signal requests delivery of an OS signal to the session. Not every
	// transport can honor this — the go-plugin transport, for one, has no
	// out-of-band signal message on the wire (interactive interrupts ride
	// as raw stdin bytes under the terminal's own raw mode) and returns a
	// non-nil error rather than silently dropping the request. No
	// above-the-seam caller invokes this today; it exists so a future
	// transport that CAN honor it (host-pty, via a real process signal)
	// has somewhere to plug in.
	Signal(sig os.Signal) error

	// Wait blocks until the session ends and returns its exit status.
	Wait() (ExitStatus, error)
}

// Launcher starts an interactive Session against an already-established
// backend connection. One implementation ships today: internal/vpio/goplugin.
type Launcher interface {
	Start(ctx context.Context, spec ProcessSpec) (Session, error)
}
