//go:build !windows

package ptyrunner

import (
	"os/exec"

	"github.com/aymanbagabas/go-pty"
)

// adjustPtyCommand is a no-op on non-Windows platforms.
func adjustPtyCommand(_ *pty.Cmd, _ *exec.Cmd) {}
