//go:build !linux

package hostpty

import "syscall"

// armDeathSignal is Linux-only (PR_SET_PDEATHSIG); elsewhere the originator's
// own Kill is the only teardown.
func armDeathSignal(*syscall.SysProcAttr) {}
