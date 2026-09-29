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
	"time"

	"github.com/creack/pty"
)

// Session is a live child on a pty. The caller reads and writes Master() as
// the terminal, calls Resize on each size change, and Wait for the exit. The
// child is reaped in the background the moment it exits (Exited); Wait is
// the release point: it closes the master. A caller that wants the child's
// last bytes drains the master to EIO before Wait — the kernel delivers them
// ahead of EIO once the slave's last holder is gone, and closing the master
// first would discard them. Drain it CONCURRENTLY with waiting for the exit:
// on macOS (a BSD tty) the child's last close of the slave blocks until its
// output has been read, so a caller that waits for Exited before reading
// deadlocks there, where Linux would let the bytes outlive the reap.
type Session struct {
	master    *os.File
	cmd       *exec.Cmd
	stopCtx   func()
	exited    chan struct{}
	ended     chan struct{} // closed once terminate has returned
	closeOnce sync.Once
	code      int
	waitErr   error
}

// endGrace bounds how long an orderly end waits for the child after SIGTERM
// before it is SIGKILLed: long enough for a runner's teardown, short enough
// that a wedged one cannot stall the party ending it.
const endGrace = 10 * time.Second

// ErrNoCommand refuses a Start with nothing to run.
var ErrNoCommand = errors.New("hostpty: no command to start")

// Start opens a pty, starts cmd on the SLAVE (its stdin/stdout/stderr become
// the terminal), and returns the Session holding the master. A cancelled ctx
// ends the child (terminate); teardown otherwise is Wait (or Kill). The child's stdio
// must not be pre-wired — Start owns it.
func Start(ctx context.Context, cmd *exec.Cmd) (*Session, error) {
	return start(ctx, cmd, endGrace)
}

func start(ctx context.Context, cmd *exec.Cmd, grace time.Duration) (*Session, error) {
	if cmd == nil {
		return nil, ErrNoCommand
	}
	attr := sessionAttrs()
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
	s := &Session{master: master, cmd: cmd, stopCtx: cancel, exited: make(chan struct{}), ended: make(chan struct{})}
	go func() {
		defer close(s.ended)
		<-ctx.Done()
		// The ctx is the "ask to end" handle, not the teardown handle: on
		// cancellation end the child so a parked pty read cannot outlive the
		// caller. The reaper still owns the reap.
		s.terminate(grace)
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

// ExitErr blocks until the child has been reaped and reports how it exited:
// nil for status 0, else an error naming the status or the wait failure.
// Unlike Wait it leaves the master OPEN, so a party that only needs to know
// the child is gone — the coordinator's dial-home race — cannot discard the
// bytes the drive has yet to read. Safe to call more than once, concurrently.
func (s *Session) ExitErr() error {
	<-s.exited
	if s.waitErr != nil {
		return s.waitErr
	}
	if s.code != 0 {
		return fmt.Errorf("runner exited with status %d", s.code)
	}
	return nil
}

// End ends the child and leaves the master open: the handle for a party that
// ends the run while another still reads its output. It returns once the
// child has exited or, past the grace, been SIGKILLed (see terminate) — not
// once it is reaped: on macOS a killed child's exit still waits for its
// output to be read, and End's caller is not the reader, so waiting for the
// reap there deadlocks. Exited, ExitErr and Wait observe the reap. The
// child's last bytes stay readable to EIO, and Wait or Kill releases the
// master.
func (s *Session) End() {
	s.stopCtx()
	<-s.ended
}

// terminate asks the child to end with SIGTERM, so a runner unwinds through
// its own teardown (PaneHost.Stop, temp cleanup), and SIGKILLs it if it is
// still running once grace has passed. A child that cannot be signalled —
// already gone, or never started — gets the kill straight away, which is a
// no-op for the first and the right end for anything else.
func (s *Session) terminate(grace time.Duration) {
	p := s.cmd.Process
	if p == nil {
		return
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		_ = p.Kill()
		return
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.exited:
	case <-timer.C:
		_ = p.Kill()
	}
}

// Kill force-ends the child and releases the pty. Safe after Wait.
func (s *Session) Kill() {
	s.End()
	s.closeOnce.Do(func() { _ = s.master.Close() })
}
