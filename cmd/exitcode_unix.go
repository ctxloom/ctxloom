//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// childExitCode maps a finished child's exec.ExitError to a shell-conventional
// exit code. exec.ExitError.ExitCode() reports -1 for a signal death, which
// os.Exit would render as 255; the convention is 128+signal (e.g. 143 for
// SIGTERM), so that's what we propagate. Anything else unexpected (no
// WaitStatus, negative code) degrades to a plain failure code.
func childExitCode(err *exec.ExitError) int {
	if ws, ok := err.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := err.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
