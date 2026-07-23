//go:build !windows

package ptyrunner

import (
	"errors"
	"io/fs"
	"os/exec"
	"syscall"

	"github.com/aymanbagabas/go-pty"
)

// isBenignPTYError reports whether err from c.Wait is the expected fallout of
// closing the PTY after the command already exited, rather than a real
// failure. On Unix the kernel reports a closed PTY master read as EIO, and a
// double-close as fs.ErrClosed. Matched by sentinel via errors.Is — never by
// substring of the error text.
func isBenignPTYError(err error) bool {
	return errors.Is(err, fs.ErrClosed) || errors.Is(err, syscall.EIO)
}

// adjustPtyCommand is a no-op on non-Windows platforms.
func adjustPtyCommand(_ *pty.Cmd, _ *exec.Cmd) {}

// pendingPTYBytes is implemented per-GOOS: golang.org/x/sys/unix does not
// define a common ioctl request number across Unix flavors (Linux's TIOCINQ
// isn't defined for darwin at all, and this pinned module version has no
// FIONREAD constant on any platform) — see prepare_ioctl_linux.go and
// prepare_ioctl_darwin.go.
