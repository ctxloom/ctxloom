//go:build !windows

package ptyrunner

import (
	"os"
	"os/exec"

	"github.com/aymanbagabas/go-pty"
)

// adjustPtyCommand is a no-op on non-Windows platforms.
func adjustPtyCommand(_ *pty.Cmd, _ *exec.Cmd) {}

// resetTerminal restores terminal to sane state after subprocess exits.
// term.State doesn't capture all attributes (like ONLCR), so we use
// stty sane to fix any line discipline corruption.
func resetTerminal() {
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}
