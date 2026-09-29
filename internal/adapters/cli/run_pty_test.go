package cli

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stuckTTY is a runnerTTY whose runner never exits on its own: only End or
// Kill end it, the way a live engine on a real pty outlives a terminal that
// went away. endEnds=false models a runner End cannot end (a wedged container
// relay), which only Kill releases.
type stuckTTY struct {
	r       *io.PipeReader
	w       *io.PipeWriter
	exited  chan struct{}
	once    sync.Once
	endEnds bool
	ended   atomic.Bool
	killed  atomic.Bool
}

func newStuckTTY(endEnds bool) *stuckTTY {
	r, w := io.Pipe()
	return &stuckTTY{r: r, w: w, exited: make(chan struct{}), endEnds: endEnds}
}

func (s *stuckTTY) Master() io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{s.r, io.Discard}
}
func (s *stuckTTY) Resize(uint16, uint16) error { return nil }
func (s *stuckTTY) Exited() <-chan struct{}     { return s.exited }
func (s *stuckTTY) Wait() (int, error)          { <-s.exited; return 0, nil }
func (s *stuckTTY) exit() {
	s.once.Do(func() { close(s.exited); _ = s.w.CloseWithError(io.EOF) })
}
func (s *stuckTTY) End() {
	s.ended.Store(true)
	if s.endEnds {
		s.exit()
	}
}
func (s *stuckTTY) Kill() { s.killed.Store(true); s.exit() }

// noTerminal keeps the drive off the test process's own stdin, which may be a
// real terminal when the suite runs from one.
func noTerminal(t *testing.T) {
	t.Helper()
	prev := termIsTerminal
	termIsTerminal = func(int) bool { return false }
	t.Cleanup(func() { termIsTerminal = prev })
}

// driveWithin runs the drive and fails the test if it has not returned in
// bound — the defect under test is a drive that never returns.
func driveWithin(t *testing.T, st *runState, bound time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- st.driveOwnedInteractive() }()
	select {
	case err := <-done:
		return err
	case <-time.After(bound):
		t.Fatalf("driveOwnedInteractive did not return within %s", bound)
		return nil
	}
}

// A signalled run (SIGTERM, SIGHUP from a terminal that went away) cancels
// the run's ctx; the drive must end the runner and return rather than wait
// for a runner that will never exit on its own.
func TestDriveOwnedInteractive_CancelledCtxEndsTheRunnerAndReturns(t *testing.T) {
	noTerminal(t)
	ctx, cancel := context.WithCancel(context.Background())
	tty := newStuckTTY(true)
	st := &runState{ctx: ctx, pty: tty}
	cancel()

	err := driveWithin(t, st, 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, tty.ended.Load(), "the runner must be ended on cancellation")
}

// The drain ending while the runner lives means the output side is gone (the
// terminal's writes fail after a hangup); nothing can render the runner any
// more, so the drive ends it instead of parking in Wait forever.
func TestDriveOwnedInteractive_DrainEndedUnderALiveRunnerEndsIt(t *testing.T) {
	noTerminal(t)
	tty := newStuckTTY(true)
	_ = tty.w.CloseWithError(errors.New("write /dev/stdout: input/output error"))
	st := &runState{ctx: context.Background(), pty: tty}

	err := driveWithin(t, st, runnerExitAfterDrain+runnerEndGrace+5*time.Second)
	require.ErrorIs(t, err, errSessionOutputLost)
	require.True(t, tty.ended.Load(), "the runner must be ended once its output cannot be drained")
}

// A runner End cannot end (a wedged relay) is released by Kill after
// runnerEndGrace, so a signalled drive is bounded even then.
func TestDriveOwnedInteractive_EndThatDoesNotEndEscalatesToKill(t *testing.T) {
	noTerminal(t)
	prev := runnerEndGrace
	runnerEndGrace = 50 * time.Millisecond
	t.Cleanup(func() { runnerEndGrace = prev })

	ctx, cancel := context.WithCancel(context.Background())
	tty := newStuckTTY(false)
	st := &runState{ctx: ctx, pty: tty}
	cancel()

	err := driveWithin(t, st, 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, tty.ended.Load())
	require.True(t, tty.killed.Load(), "a runner End did not end must be Killed")
}
