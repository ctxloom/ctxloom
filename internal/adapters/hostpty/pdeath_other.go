//go:build !linux && !windows

package hostpty

import "syscall"

// armDeathSignal is Linux-only (PR_SET_PDEATHSIG). Elsewhere the runner
// watches its parent itself (parentwatch.WithParent), and the originator's
// death also hangs up the pty, which SIGHUPs the runner as session leader.
func armDeathSignal(*syscall.SysProcAttr) {}
