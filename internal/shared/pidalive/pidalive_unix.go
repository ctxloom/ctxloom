//go:build !windows

package pidalive

import (
	"errors"
	"os"
	"syscall"
)

// Probe reports pid's liveness via a signal-0 probe (no signal is actually
// delivered — see kill(2)). EPERM means the process exists but this user
// cannot signal it — confidently Alive. "No such process" is confidently
// Dead: os.ErrProcessDone, not a raw syscall.ESRCH, is what actually surfaces
// here on a modern Go/Linux toolchain (os.FindProcess opens a pidfd via
// pidfd_open, and BOTH that path and the classic kill(2) fallback wrap ESRCH
// into os.ErrProcessDone before returning it — see os/exec_unix.go's
// convertESRCH, which findProcess and Process.Signal both route through) —
// checked alongside the raw errno for older toolchains/other unix targets
// that might still surface it unwrapped. Any other, undocumented outcome is
// a platform surprise this probe cannot interpret, and it says so (Unsure)
// rather than guessing.
func Probe(pid int) State {
	// os.FindProcess is documented to always succeed on Unix regardless of
	// whether pid names a live process ("On Unix systems, FindProcess always
	// succeeds..."), so an error-return branch here was provably unreachable
	// dead code — and worse, it read as if this probe handled a FindProcess
	// failure it can never actually receive (U114-F03).
	p, _ := os.FindProcess(pid)
	return classifySignalErr(p.Signal(syscall.Signal(0)))
}

// classifySignalErr turns a signal-0 outcome into a verdict.
//
// Every comparison is errors.Is, not ==. The runtime happens to hand back a
// bare syscall.Errno today, so == would work, but that is a property of the
// standard library's internals rather than a documented contract: os.Signal
// already routes both the pidfd and the classic kill(2) path through
// convertESRCH, so the shape of what arrives here is chosen elsewhere and can
// change under us. An == comparison against a wrapped errno silently falls
// through to Unsure, and Unsure means "err toward alive" — a live-looking
// verdict for a process that is confidently gone, which is how a stale lock or
// an orphaned worktree becomes permanently unreclaimable.
func classifySignalErr(err error) State {
	switch {
	case err == nil:
		return Alive
	case errors.Is(err, syscall.EPERM):
		return Alive
	case errors.Is(err, syscall.ESRCH), errors.Is(err, os.ErrProcessDone):
		return Dead
	default:
		return Unsure
	}
}
