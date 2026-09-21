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
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// Session is a live child on a pty. The caller reads and writes Master() as
// the terminal, calls Resize on each size change, and Wait for the exit. Wait
// is the release point: it closes the master and reaps the child.
type Session struct {
	master   *os.File
	cmd      *exec.Cmd
	stopCtx  func()
	waitOnce sync.Once
	code     int
	waitErr  error
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
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{master: master, cmd: cmd, stopCtx: cancel}
	go func() {
		<-ctx.Done()
		// The ctx is the "ask to end" handle, not the teardown handle: on
		// cancellation kill the child so a parked pty read cannot outlive the
		// caller. Wait still owns the reap.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	return s, nil
}

// Master is the pty master: the terminal the frontend reads and writes.
func (s *Session) Master() io.ReadWriter { return s.master }

// Resize applies a new terminal size to the pty, which the kernel delivers to
// the child as SIGWINCH — the size an engine reads back is the size set here.
func (s *Session) Resize(rows, cols uint16) error {
	return pty.Setsize(s.master, &pty.Winsize{Rows: rows, Cols: cols})
}

// Wait blocks until the child exits, closes the master, and returns the exit
// code. Idempotent: the terminal result is delivered once and cached.
func (s *Session) Wait() (int, error) {
	s.waitOnce.Do(func() {
		werr := s.cmd.Wait()
		_ = s.master.Close()
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
	})
	return s.code, s.waitErr
}

// Kill force-ends the child and releases the pty. Safe after Wait.
func (s *Session) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.stopCtx()
	_ = s.master.Close()
}
