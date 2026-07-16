//go:build !windows

package ptyrunner

import (
	"errors"
	"io/fs"
	"os/exec"
	"syscall"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
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

// pendingPTYBytes reports how many bytes are currently buffered and unread
// on the pty master's read side via TIOCINQ (the terminal-device ioctl —
// same request number as FIONREAD, which golang.org/x/sys/unix does not
// define a named constant for at all; unix.FIONREAD does not exist in this
// module) — RunInteractive's drainPTY (ptyrunner.go) polls this instead of
// sleeping blindly to detect "nothing left to drain" with no added latency
// in the common case. ok is false when ptty is unexpectedly not a UnixPty
// or the ioctl itself fails, in which case the caller falls back to its own
// bounded wait.
func pendingPTYBytes(ptty pty.Pty) (int, bool) {
	up, ok := ptty.(pty.UnixPty)
	if !ok {
		return 0, false
	}
	n, err := unix.IoctlGetInt(int(up.Master().Fd()), unix.TIOCINQ)
	if err != nil {
		return 0, false
	}
	return n, true
}
