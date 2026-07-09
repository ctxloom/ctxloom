//go:build windows

package cli

import "os/exec"

// childExitCode maps a finished child's exec.ExitError to an exit code.
// Windows has no signal deaths; an unexpected negative code (ExitCode reports
// -1 when the process hasn't exited) degrades to a plain failure code.
func childExitCode(err *exec.ExitError) int {
	if code := err.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
