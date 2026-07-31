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
// internal/vpio/goplugin was the FIRST implementation, wrapping the existing
// hashicorp/go-plugin-backed bidirectional Run RPC (internal/lm/grpc,
// llm.proto's `Run`) — a MOVE of the pump/relay logic that used to live
// inline at the call site, not a rewrite of the wire protocol. This package
// does not rename github.com/hashicorp/go-plugin (the upstream dependency)
// or the llm.proto Run RPC; it wraps them.
//
// internal/vpio/dockerexec has SINCE shipped (docker exec -it against an
// already-running container's process, for the container-isolation
// runtime) — the interface was shaped so a second implementation could plug
// in without any above-the-seam caller changing, and it did. Only host-pty
// (a bare local process on a host pty, for a non-plugin engine) remains
// future work.
package vpio

import (
	"context"
	"io"
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
	// Stdout receives the session's output. REQUIRED: Start must REFUSE a nil
	// Stdout rather than substitute a discard, because a turn whose entire
	// output vanished behind exit code 0 is indistinguishable from one that
	// ran and said nothing. On a transport that carries the turn over a
	// single stream, this also receives the session's stderr — see Stderr.
	Stdout io.Writer
	// Stderr receives DIAGNOSTICS ABOUT the session. Whether it also receives
	// the session's own stderr depends on what the transport can separate, and
	// a caller must not assume it does:
	//
	//   - goplugin carries stdout and stderr as distinct wire frames, so the
	//     engine's stderr arrives here.
	//   - dockerexec runs the turn on a pty, which has ONE stream, so the
	//     kernel interleaves the session's stderr into Stdout and only this
	//     transport's own warnings arrive here.
	//
	// A caller that must see everything the session emitted therefore reads
	// Stdout; Stderr is where the transport explains itself.
	//
	// nil is permitted only for a transport that does NOT deliver the
	// session's own stderr here — it then has only its own diagnostics to
	// place, and falls back to the process diagnostic sink. A transport that
	// DOES deliver the session's stderr here must refuse a nil Stderr, for the
	// same reason Stdout is required: discarding half the session is not a
	// sanctioned way to run one. So goplugin refuses it and dockerexec
	// tolerates it.
	Stderr io.Writer
}

// ExitStatus is the terminal result of a Session.
type ExitStatus struct {
	// Code is the session's exit code (0 = success).
	Code int32
}

// Session is a started interactive turn: the stdio wired at Start, plus the
// control primitives above-the-seam code drives it with.
//
// LIFECYCLE. There is deliberately no Close/Release: Wait IS the release
// point, and a caller that reaches Start MUST reach Wait. Every resource a
// transport holds for the turn — pty master, pump goroutines, the resize
// channel — is freed there, whether the turn ended normally or was cut short.
// Cancelling the ctx passed to Start ASKS the turn to end; it does not
// substitute for Wait, and a session abandoned without one can still be
// holding host resources. Pinned per transport by
// goplugin's TestSession_WaitAloneReleasesResourcesWithoutCtxCancellation and
// dockerexec's TestSession_WaitClosesThePtyMaster.
//
// After Wait returns, the remaining methods stay SAFE rather than becoming
// undefined: Resize on a finished session is a silent no-op, and Wait itself
// is idempotent (see below).
type Session interface {
	// Resize notifies the session of a terminal size change (rows, cols).
	// Above-the-seam code calls this once per SIGWINCH-derived size event;
	// implementations must not block the caller on a slow or finished
	// transport (coalesce/drop rather than stall the resize pump).
	Resize(rows, cols uint32)

	// Wait blocks until the session ends and returns its exit status.
	//
	// IDEMPOTENT, and required to be: the terminal result is delivered once
	// and cached, so every later call returns that same status and error
	// rather than parking on something nothing will produce a second time.
	// This is the one multiplicity question a caller cannot answer by
	// inspection — the natural `defer session.Wait()` next to an explicit
	// one either works everywhere or deadlocks depending on which transport
	// was selected — so it is settled here rather than per implementation.
	// Both shipping transports pin it (TestSession_WaitIsIdempotent, in
	// goplugin and dockerexec alike).
	Wait() (ExitStatus, error)
}

// Launcher starts an interactive Session against an already-established
// backend connection. Two implementations ship today: internal/vpio/goplugin
// (the go-plugin Run RPC transport) and internal/vpio/dockerexec (the
// `docker exec -it` transport for the container-isolation runtime); only
// host-pty remains future work.
type Launcher interface {
	Start(ctx context.Context, spec ProcessSpec) (Session, error)
}
