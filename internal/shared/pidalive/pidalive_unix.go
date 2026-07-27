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
	p, err := os.FindProcess(pid)
	if err != nil {
		// os.FindProcess is documented never to fail on Unix outright (see
		// findProcess's fallback-to-PID behavior for pidfd errors other than
		// ESRCH) — an error here would be a platform surprise this probe
		// cannot interpret.
		return Unsure
	}
	switch serr := p.Signal(syscall.Signal(0)); {
	case serr == nil:
		return Alive
	case serr == syscall.EPERM:
		return Alive
	case serr == syscall.ESRCH, errors.Is(serr, os.ErrProcessDone):
		return Dead
	default:
		return Unsure
	}
}
