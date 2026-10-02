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
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/aymanbagabas/go-pty"

	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
	"github.com/ctxloom/ctxloom/internal/shared/ptyrunner"
)

// Session is a live child on a pty: the port the interactive frontend drives,
// blind to where the child runs. The caller reads and writes Master() as the
// terminal, calls Resize on each size change, and Wait for the exit. The
// child is reaped in the background the moment it exits (Exited); Wait is
// the release point: it closes the master. A caller that wants the child's
// last bytes drains the master to its end before Wait — on a Unix pty the
// kernel delivers them ahead of EIO once the slave's last holder is gone, and
// closing the master first would discard them.
type Session interface {
	// Master is the pty master: the terminal the frontend reads and writes.
	Master() io.ReadWriter
	// Resize applies a new terminal size to the pty; the child sees it as
	// its own window size.
	Resize(rows, cols uint16) error
	// Exited is closed once the child has been reaped. The master is still
	// open then.
	Exited() <-chan struct{}
	// Wait blocks until the child has been reaped, closes the master, and
	// returns the exit code.
	Wait() (int, error)
	// ExitErr blocks until the child has been reaped and reports how it
	// exited, leaving the master OPEN.
	ExitErr() error
	// End ends the child and leaves the master open to its reader.
	End()
	// Kill force-ends the child and releases the pty.
	Kill()
}

// Starter starts cmd on a fresh pty and returns the live Session. A cancelled
// ctx ends the child; teardown otherwise is Wait (or Kill). The child's stdio
// must not be pre-wired — the Starter owns it.
type Starter func(ctx context.Context, cmd *exec.Cmd) (Session, error)

// session is the go-pty Session: a Unix pty, or a ConPTY on Windows.
type session struct {
	ptty      pty.Pty
	cmd       *pty.Cmd
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

// Start is the Starter: it opens a pty, starts cmd on it (its
// stdin/stdout/stderr become the terminal), and returns the Session holding
// the master.
func Start(ctx context.Context, cmd *exec.Cmd) (Session, error) {
	s, err := start(ctx, cmd, endGrace)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func start(ctx context.Context, cmd *exec.Cmd, grace time.Duration) (*session, error) {
	if cmd == nil {
		return nil, ErrNoCommand
	}
	ptty, err := pty.New()
	if err != nil {
		return nil, err
	}
	releaseSlave, err := prepareSlave(ptty)
	if err != nil {
		_ = ptty.Close()
		return nil, fmt.Errorf("hostpty: make the pty's input raw: %w", err)
	}
	// The child's end is owned here (Session.End's SIGTERM-then-grace), not
	// by the command's context, which therefore never cancels.
	pc := ptyrunner.Command(context.Background(), ptty, cmd)
	sessionAttrs(pc)
	if err := pc.Start(); err != nil {
		_ = ptty.Close()
		return nil, err
	}
	releaseSlave() // the child holds its own copies
	ctx, cancel := context.WithCancel(ctx)
	s := &session{ptty: ptty, cmd: pc, stopCtx: cancel, exited: make(chan struct{}), ended: make(chan struct{})}
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
		werr := pc.Wait()
		s.stopCtx()
		if werr == nil {
			return
		}
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			s.code = exitstatus.Of(ee)
			return
		}
		s.waitErr = werr
	}()
	return s, nil
}

// Exited is closed once the child has been reaped. The master is still open
// then: read it to EIO for the child's last bytes, then Wait.
func (s *session) Exited() <-chan struct{} { return s.exited }

// Master is the pty master: the terminal the frontend reads and writes.
func (s *session) Master() io.ReadWriter { return s.ptty }

// Resize applies a new terminal size to the pty, which the kernel delivers to
// the child as SIGWINCH — the size an engine reads back is the size set here.
func (s *session) Resize(rows, cols uint16) error {
	return s.ptty.Resize(int(cols), int(rows))
}

// Wait blocks until the child has been reaped, closes the master, and
// returns the exit code. Idempotent: the terminal result is delivered once
// and cached.
func (s *session) Wait() (int, error) {
	<-s.exited
	s.closeOnce.Do(func() { _ = s.ptty.Close() })
	return s.code, s.waitErr
}

// ExitErr blocks until the child has been reaped and reports how it exited:
// nil for status 0, else an error naming the status or the wait failure.
// Unlike Wait it leaves the master OPEN, so a party that only needs to know
// the child is gone — the coordinator's dial-home race — cannot discard the
// bytes the drive has yet to read. Safe to call more than once, concurrently.
func (s *session) ExitErr() error {
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
func (s *session) End() {
	s.stopCtx()
	<-s.ended
}

// terminate asks the child to end with SIGTERM, so a runner unwinds through
// its own teardown (ending the engine it hosts, temp cleanup), and SIGKILLs it if it is
// still running once grace has passed. A child that cannot be signalled —
// already gone, or never started — gets the kill straight away, which is a
// no-op for the first and the right end for anything else.
func (s *session) terminate(grace time.Duration) {
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
func (s *session) Kill() {
	s.End()
	s.closeOnce.Do(func() { _ = s.ptty.Close() })
}
