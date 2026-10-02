//go:build !windows

package hostpty

import (
	"errors"
	"os"
	"syscall"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// errNoSlave refuses a pty whose slave end this process cannot reach: go-pty
// hands back a UnixPty on every Unix, so this is a library contract broken,
// not a platform to degrade on.
var errNoSlave = errors.New("hostpty: the pty exposes no slave end")

// sessionAttrs makes the child a session leader on the slave (its controlling
// terminal) that dies with this process: a runner that outlived a hard-killed
// originator would hold the engine, the endpoint and the session lock with
// nobody to tear it down.
func sessionAttrs(c *pty.Cmd) {
	attr := &syscall.SysProcAttr{Setsid: true, Setctty: true}
	armDeathSignal(attr)
	c.SysProcAttr = attr
}

// prepareSlave makes the slave's input raw before the child exists and
// returns the release of THIS process's slave fd, for once the child holds
// its own copies. go-pty keeps that fd open for the pty's whole life, and
// while it is open the master never reads EIO, so the child's last bytes
// would never be followed by an end.
func prepareSlave(ptty pty.Pty) (release func(), err error) {
	up, ok := ptty.(pty.UnixPty)
	if !ok {
		return nil, errNoSlave
	}
	if err := rawInput(up.Slave()); err != nil {
		return nil, err
	}
	return func() { _ = up.Slave().Close() }, nil
}

// rawInput turns off the slave's INPUT processing, before the child exists
// to read it. This pty is a byte transport: the frontend already holds the
// human's terminal in raw mode and the runner relays what it reads, verbatim,
// into the engine's own pty. A fresh pty is COOKED — the kernel echoes every
// input byte straight back out the master (terminal query replies and mouse
// reports painted as `^[[<35;…M` into the engine's prompt), holds input until
// a newline, rewrites CR to LF and turns ^C into a SIGINT aimed at the runner.
// Output processing is left alone: the runner's own diagnostics are plain
// `\n` lines and ONLCR is what renders them.
func rawInput(tty *os.File) error {
	fd := int(tty.Fd())
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	t.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}
