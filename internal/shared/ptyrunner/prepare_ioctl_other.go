//go:build !linux && !darwin && !windows

package ptyrunner

import "github.com/aymanbagabas/go-pty"

// pendingPTYBytes has no pending-bytes ioctl wired up on this platform, so
// RunInteractive's drainPTY falls back to its bounded wait, exactly as it does
// on Windows. Without it the package does not compile outside linux, darwin
// and windows.
func pendingPTYBytes(_ pty.Pty) (int, bool) {
	return 0, false
}
