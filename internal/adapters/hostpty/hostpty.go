// Package hostpty is the ORIGINATOR side of an interactive turn on the human's
// machine: it starts a child on a pseudo-terminal and hands the caller the
// MASTER end. The runner (`ctxloom runner`) is the child; the originator holds
// the master, wraps it with the terminal layer (adapters/termui) and pumps
// SIGWINCH-derived resizes onto it. Resize and signals reach the engine through
// the pty the kernel carries, not a proxied stream — which is the whole reason
// the runner is spawned on a pty rather than behind a pipe.
//
// This is the host counterpart of adapters/attach, which owns the SAME shape
// for a container (the docker CLI's -it attachment owns the pty there). Both
// exist so the frontend drives one interactive Session interface, blind to
// where the runner runs.
package hostpty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Session is a live child on a pty. The caller reads and writes Master() as
// the terminal, calls Resize on each size change, and Wait for the exit. The
// child is reaped in the background the moment it exits (Exited); Wait is
// the release point: it closes the master. A caller that wants the child's
// last bytes drains the master to EIO before Wait — the kernel delivers them
// ahead of EIO once the slave's last holder is gone, and closing the master
// first would discard them.
type Session struct {
	master    *os.File
	cmd       *exec.Cmd
	stopCtx   func()
	exited    chan struct{}
	closeOnce sync.Once
	code      int
	waitErr   error
}

// ErrNoCommand refuses a Start with nothing to run.
var ErrNoCommand = errors.New("hostpty: no command to start")

// Start opens a pty, starts cmd on the SLAVE (its stdin/stdout/stderr become
// the terminal), and returns the Session holding the master. A cancelled ctx
// kills the child; teardown otherwise is Wait (or Kill). The child's stdio
// must not be pre-wired — Start owns it.
func Start(ctx context.Context, cmd *exec.Cmd) (*Session, error) {
	if cmd == nil {
		return nil, ErrNoCommand
	}
	// The child is a session leader on the slave (its controlling terminal)
	// and dies with this process: a runner that outlived a hard-killed
	// originator would hold the engine, the endpoint and the session lock
	// with nobody to tear it down.
	attr := &syscall.SysProcAttr{Setsid: true, Setctty: true}
	armDeathSignal(attr)
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = slave.Close() }() // the child holds its own copies
	if err := rawInput(slave); err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("hostpty: make the pty's input raw: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{master: master, cmd: cmd, stopCtx: cancel, exited: make(chan struct{})}
	go func() {
		<-ctx.Done()
		// The ctx is the "ask to end" handle, not the teardown handle: on
		// cancellation kill the child so a parked pty read cannot outlive the
		// caller. The reaper still owns the reap.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	// Reap in the background, exactly once: a Start()ed process is released
	// from the process table only by Wait, and the master stays open across
	// the reap so the child's last bytes are still readable.
	go func() {
		defer close(s.exited)
		werr := cmd.Wait()
		s.stopCtx()
		if werr == nil {
			return
		}
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			s.code = ee.ExitCode()
			return
		}
		s.waitErr = werr
	}()
	return s, nil
}

// rawInput turns off the slave's INPUT processing, before the child exists
// to read it. This pty is a byte transport: the frontend already holds the
// human's terminal in raw mode and the runner relays what it reads, verbatim,
// into the engine's own pane. A fresh pty is COOKED — the kernel echoes every
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

// Exited is closed once the child has been reaped. The master is still open
// then: read it to EIO for the child's last bytes, then Wait.
func (s *Session) Exited() <-chan struct{} { return s.exited }

// Master is the pty master: the terminal the frontend reads and writes.
func (s *Session) Master() io.ReadWriter { return s.master }

// Resize applies a new terminal size to the pty, which the kernel delivers to
// the child as SIGWINCH — the size an engine reads back is the size set here.
func (s *Session) Resize(rows, cols uint16) error {
	return pty.Setsize(s.master, &pty.Winsize{Rows: rows, Cols: cols})
}

// Wait blocks until the child has been reaped, closes the master, and
// returns the exit code. Idempotent: the terminal result is delivered once
// and cached.
func (s *Session) Wait() (int, error) {
	<-s.exited
	s.closeOnce.Do(func() { _ = s.master.Close() })
	return s.code, s.waitErr
}

// Kill force-ends the child and releases the pty. Safe after Wait.
func (s *Session) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.stopCtx()
	s.closeOnce.Do(func() { _ = s.master.Close() })
}
