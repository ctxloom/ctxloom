//go:build darwin

package ptyrunner

import (
	"github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// fionread is the FIONREAD ioctl request number (bytes available to read on
// a fd), per BSD's <sys/filio.h>: _IOR('f', 127, sizeof(int)) = 0x4004667f.
// The value is fixed by that macro for both darwin/amd64 and darwin/arm64
// (sizeof(int) is 4 on either), so it's safe as an untyped constant here.
// golang.org/x/sys/unix (pinned at v0.46.0, see go.mod) exports neither
// FIONREAD nor TIOCINQ for darwin, so the request number is declared
// locally rather than borrowed from unix — this is the constant that was
// missing, breaking the darwin link for the whole module (prepare_other.go
// used unix.TIOCINQ, a Linux-only ioctl; see prepare_ioctl_linux.go).
const fionread = 0x4004667f

// pendingPTYBytes reports how many bytes are currently buffered and unread
// on the pty master's read side via FIONREAD, the BSD/Darwin equivalent of
// Linux's TIOCINQ (see prepare_ioctl_linux.go) — RunInteractive's drainPTY
// (ptyrunner.go) polls this instead of sleeping blindly to detect "nothing
// left to drain" with no added latency in the common case. ok is false when
// ptty is unexpectedly not a UnixPty or the ioctl itself fails, in which
// case the caller falls back to its own bounded wait.
func pendingPTYBytes(ptty pty.Pty) (int, bool) {
	up, ok := ptty.(pty.UnixPty)
	if !ok {
		return 0, false
	}
	n, err := unix.IoctlGetInt(int(up.Master().Fd()), fionread)
	if err != nil {
		return 0, false
	}
	return n, true
}
