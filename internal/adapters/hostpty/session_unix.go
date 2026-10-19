//go:build !windows

package hostpty

import (
	"errors"
	"syscall"

	"github.com/aymanbagabas/go-pty"

	"github.com/ctxloom/ctxloom/internal/shared/ptyrunner"
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
//
// The input is made raw because the frontend already holds the human's
// terminal in raw mode and writes what is typed verbatim, and until an engine
// takes this terminal over — while the runner starts, and after the engine
// has exited — what arrives must reach whoever reads next untouched. A fresh
// pty is COOKED: the kernel echoes every input byte straight back out the
// master (terminal query replies and mouse reports painted as `^[[<35;…M`
// into the engine's prompt), holds input until a newline, rewrites CR to LF
// and turns ^C into a SIGINT aimed at the runner. The runner gives the engine
// a new terminal's line discipline for its own lifetime
// (ptyrunner.FreshLineDiscipline) and puts this one back after it.
func prepareSlave(ptty pty.Pty) (release func(), err error) {
	up, ok := ptty.(pty.UnixPty)
	if !ok {
		return nil, errNoSlave
	}
	if err := ptyrunner.RawInput(up.Slave()); err != nil {
		return nil, err
	}
	return func() { _ = up.Slave().Close() }, nil
}
