//go:build linux

package ptyrunner

import (
	"github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// pendingPTYBytes reports how many bytes are currently buffered and unread
// on the pty master's read side via TIOCINQ (the terminal-device ioctl —
// same request number as FIONREAD on Linux, where the two are synonyms;
// golang.org/x/sys/unix does not define a FIONREAD constant at all here,
// only TIOCINQ, and TIOCINQ itself is Linux-only in this module — see
// prepare_ioctl_darwin.go for the darwin equivalent) — RunInteractive's
// drainPTY (ptyrunner.go) polls this instead of sleeping blindly to detect
// "nothing left to drain" with no added latency in the common case. ok is
// false when ptty is unexpectedly not a UnixPty or the ioctl itself fails,
// in which case the caller falls back to its own bounded wait.
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
