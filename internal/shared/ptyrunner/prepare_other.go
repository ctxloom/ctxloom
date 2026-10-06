//go:build !windows

package ptyrunner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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

// pendingPTYBytes is implemented per-GOOS: golang.org/x/sys/unix does not
// define a common ioctl request number across Unix flavors (Linux's TIOCINQ
// isn't defined for darwin at all, and this pinned module version has no
// FIONREAD constant on any platform) — see prepare_ioctl_linux.go and
// prepare_ioctl_darwin.go.

// pollableMaster returns a second descriptor for the pty master, opened
// non-blocking so the runtime poller owns it: a Read parked on it is woken by
// its Close.
//
// go-pty's own master cannot be that file. creack/pty, which allocates it,
// sizes the pty through (*os.File).Fd, and Fd pins the file in blocking mode
// for good; a Read on a blocking file sits in the read(2) syscall, where
// Close cannot reach it — the descriptor is only released once that read
// returns. A pty master's read returns only when every holder of the slave
// has let go, so a process the child left behind holding the slave (an MCP
// server or a tool of an engine that crashed) would hold the read, and every
// caller joined on it, for as long as that process lives.
//
// The duplicate shares the master's open file description, so O_NONBLOCK
// set here applies to go-pty's handle too; nothing reads or writes through
// that handle once this view exists — only ioctls (resize, FIONREAD) and the
// final close use it. The duplicate is close-on-exec, so the child never
// inherits a handle on its own master.
func pollableMaster(ptty pty.Pty) (io.ReadWriteCloser, error) {
	up, ok := ptty.(pty.UnixPty)
	if !ok {
		return nil, fmt.Errorf("pty %T exposes no master descriptor", ptty)
	}
	dup, dupErr := -1, error(nil)
	if err := up.Control(func(fd uintptr) { dup, dupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0) }); err != nil {
		return nil, err
	}
	if dupErr != nil {
		return nil, fmt.Errorf("duplicate the pty master: %w", dupErr)
	}
	if err := unix.SetNonblock(dup, true); err != nil {
		_ = unix.Close(dup)
		return nil, fmt.Errorf("make the pty master non-blocking: %w", err)
	}
	return os.NewFile(uintptr(dup), up.Master().Name()), nil
}
