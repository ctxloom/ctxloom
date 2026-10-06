package testenv

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// ptyGracefulShutdown bounds how long Close waits after SIGTERM before
// escalating to SIGKILL. Generous relative to a mock backend's near-instant
// shutdown, short relative to a test suite: this only pays the cost on the
// (already off the happy path) case of closing a session that didn't exit on
// its own.
const ptyGracefulShutdown = 2 * time.Second

// deadlineMargin is how far ahead of the test binary's deadline BudgetUntil
// ends: room for the failing assertion to print what the wait saw before go
// test's own timeout panics over it.
const deadlineMargin = 10 * time.Second

// noDeadline stands in for a bound when there is no deadline to honour: longer
// than any run, yet short enough that a callee converting it to milliseconds
// and back to nanoseconds (a poll(2) timeout) does not overflow.
const noDeadline = 100 * 365 * 24 * time.Hour

// BudgetUntil is how long a wait on a pty or process event may take when the
// only bound is deadline (ok reports whether there is one): until
// deadlineMargin before it, or noDeadline without one. A wait for bytes
// crossing a real pty, or for a real process to exit, carries no deadline of
// its own: a loaded machine delays those by any amount, and a deadline short
// enough to matter fails on an event that was merely late.
func BudgetUntil(deadline time.Time, ok bool) time.Duration {
	if !ok {
		return noDeadline
	}
	return time.Until(deadline) - deadlineMargin
}

// TestBudget is BudgetUntil the test binary's own deadline, for a wait that
// takes a duration.
func TestBudget(t *testing.T) time.Duration {
	return BudgetUntil(t.Deadline())
}

// TestExpiry is TestBudget as a channel that fires when it runs out.
func TestExpiry(t *testing.T) <-chan time.Time {
	return time.After(TestBudget(t))
}

// ptyCapture is a goroutine-safe accumulator for everything read off a pty's
// master side: the pty's own io.Copy-draining goroutine writes into it while
// the test reads it back at will. Same idiom as internal/adapters/termui's
// overlay_composition_test.go syncBuf / internal/adapters/cli/tui's overlay_test.go
// syncBuffer — re-derived here (rather than exported and reused) because this
// one backs a real subprocess's pty, not an in-process pty pair, and those
// types are unexported to a different package besides.
//
// Every write is an event a wait wakes on.
type ptyCapture struct {
	mu      sync.Mutex
	b       strings.Builder
	written chan struct{} // closed by the next write
}

func (c *ptyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.written != nil {
		close(c.written)
		c.written = nil
	}
	return c.b.Write(p)
}

// next returns what has been captured and a channel the next write closes.
func (c *ptyCapture) next() (string, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.written == nil {
		c.written = make(chan struct{})
	}
	return c.b.String(), c.written
}

// await re-checks cond on every write until it holds, and reports false only
// once expired fires (never, for a nil expired). It returns what was captured
// when it decided.
func (c *ptyCapture) await(expired <-chan time.Time, cond func(string) bool) (string, bool) {
	for {
		cur, written := c.next()
		if cond(cur) {
			return cur, true
		}
		select {
		case <-written:
		case <-expired:
			return c.String(), false
		}
	}
}

func (c *ptyCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// PTYSession is a live ctxloom subprocess attached to a real pty. Unlike Run/
// RunWithStdin (which run a command to completion and capture the result), a
// PTYSession stays around so the caller can write keystrokes into it —
// including the Ctrl-] viewer prefix (0x1d) — and wait on the
// accumulated output for escape-sequence signatures while (or after) the
// process runs.
type PTYSession struct {
	pty pty.Pty
	cmd *pty.Cmd
	out *ptyCapture

	exited  chan struct{}
	exitErr error
}

// RunPTY starts ctxloom attached to a real pty sized cols x rows, with the
// same isolated HOME/XDG environment Run/RunWithStdin use, in the project
// directory. extraEnv is appended on top of that isolated environment —
// mirroring TestEnvironment.Command's extraEnv parameter — for callers that
// need to steer an in-process fixture (e.g. CTXLOOM_MOCK_ECHO_STDIN=1) rather
// than the target of a real pty session; nil is the common case.
//
// TERM is fixed to "dumb". With a real TERM value (e.g. "xterm-256color"),
// ctxloom's startup color-profile detection (muesli/termenv, reached via
// lipgloss) queries the terminal's background color (OSC 11: `\x1b]11;?`)
// and cursor position (DSR: `\x1b[6n`) whenever both stdin and stdout are a
// real tty. This harness never answers those queries — it isn't a terminal
// emulator — so with any other TERM the run blocks for termenv's multi-second
// query timeout on literally every invocation (measured: ~5.3s, dominating
// and destabilizing suite runtime). TERM=dumb steers that detection away from
// probing at all; it does not affect the surround bar/viewer's own escape
// sequences, which internal/adapters/termui writes unconditionally and never gates on
// TERM.
func (e *TestEnvironment) RunPTY(cols, rows int, extraEnv []string, args ...string) (*PTYSession, error) {
	return e.RunPTYFrom(e.AppBinary, cols, rows, extraEnv, args...)
}

// RunPTYFrom is RunPTY with the binary named by the caller instead of
// AppBinary: same pty, same directory, same isolated environment. It exists
// for a scenario that must run a session from a binary it controls the
// lifetime of (a copy it can unlink while the process lives), which
// AppBinary — shared by every scenario in the run — can never be.
func (e *TestEnvironment) RunPTYFrom(bin string, cols, rows int, extraEnv []string, args ...string) (*PTYSession, error) {
	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	if err := p.Resize(cols, rows); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("resize pty to %dx%d: %w", cols, rows, err)
	}

	cmd := p.CommandContext(context.Background(), bin, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = append(append(e.isolatedEnv(), "TERM=dumb"), extraEnv...)
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	s := &PTYSession{pty: p, cmd: cmd, out: &ptyCapture{}, exited: make(chan struct{})}
	go func() { _, _ = io.Copy(s.out, p) }()

	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go func() {
		s.exitErr = cmd.Wait()
		close(s.exited)
	}()
	return s, nil
}

// Write sends raw bytes into the pty as if typed at the terminal — e.g.
// []byte{0x1d} for the Ctrl-] viewer prefix, optionally followed by a viewer
// key or 'q'.
func (s *PTYSession) Write(p []byte) (int, error) { return s.pty.Write(p) }

// Hangup closes the terminal's master end under the live process — what a
// terminal emulator that dies does to the session it hosted: the kernel
// hangs the slave up, SIGHUP reaches the foreground process, and its tty
// reads and writes fail from then on. Close still reaps afterwards.
func (s *PTYSession) Hangup() error { return s.pty.Close() }

// Output returns everything captured off the pty so far.
func (s *PTYSession) Output() string { return s.out.String() }

// PID returns the session's top-level ctxloom process id, valid once RunPTY
// has returned successfully (cmd.Start already ran). Callers that need to
// find the process's OWN children (e.g. testenv.RunnerChildrenOf, to observe
// a spawned runner subprocess) need this — cmd itself is unexported so
// nothing outside this file can reach *os.Process directly.
func (s *PTYSession) PID() int {
	if s.cmd.Process == nil {
		return -1
	}
	return s.cmd.Process.Pid
}

// AwaitOutput re-checks cond against the accumulated output on every byte the
// pty delivers, until it holds or TestExpiry(t) fires. It returns the output
// it decided on, so a failure can show the screen it waited for.
func (s *PTYSession) AwaitOutput(t *testing.T, cond func(output string) bool) (string, bool) {
	return s.out.await(TestExpiry(t), cond)
}

// WaitForOutput is AwaitOutput bounded by timeout instead of the test's
// deadline.
func (s *PTYSession) WaitForOutput(timeout time.Duration, cond func(output string) bool) bool {
	_, ok := s.out.await(time.After(timeout), cond)
	return ok
}

// AwaitExit blocks for the process to exit, or until TestExpiry(t) fires.
// exited reports whether it did; err is its Wait error (nil for a clean exit).
func (s *PTYSession) AwaitExit(t *testing.T) (exited bool, err error) {
	return s.waitExit(TestExpiry(t))
}

// Wait is AwaitExit bounded by timeout instead of the test's deadline.
func (s *PTYSession) Wait(timeout time.Duration) (exited bool, err error) {
	return s.waitExit(time.After(timeout))
}

func (s *PTYSession) waitExit(expired <-chan time.Time) (bool, error) {
	select {
	case <-s.exited:
		return true, s.exitErr
	case <-expired:
		return false, nil
	}
}

// ExitCode reports the exited process's exit code. Valid only once Wait has
// reported exited=true; -1 if the process never started or never exited.
func (s *PTYSession) ExitCode() int {
	if s.cmd.ProcessState == nil {
		return -1
	}
	return s.cmd.ProcessState.ExitCode()
}

// Close releases the pty and, if the process is still running (e.g. an
// assertion failed before the session exited on its own), shuts it down so a
// failed test never leaks a child process — including the `ctxloom runner
// <engine>` subprocess the run starts (isolation.StartHostRunner, setsid'd
// by isolateRunner into its own session so it survives outside this
// session's process group).
//
// Graceful first: SIGTERM is exactly what cmd/run.go's own
// signal.NotifyContext(shutdownSignals) already listens for, unwinding
// through the runner's designed shutdown path (HostRunner.Kill →
// killSession), which this harness gets for free by using it. A bare SIGKILL
// bypasses that entirely: the parent never gets a chance to run its own
// cleanup, and the runner child — in a different session by construction —
// is not reachable by anything this harness could signal as a group, so a
// test whose assertion failed before the session exited would leave it
// orphaned.
//
// SIGKILL remains the fallback for a process that doesn't honor SIGTERM
// within ptyGracefulShutdown. Either way, Close finishes by explicitly
// reaping any runner child captured before the signal was sent — the pid,
// not a re-derived one, since a dead parent's former child is reparented to
// init (ppid 1) within the same instant and can no longer be found by
// ppid — so a SIGKILL'd or simply slow parent never leaves the runner behind
// even if its own graceful path didn't get there first. Safe to call after a
// natural exit.
func (s *PTYSession) Close() {
	var pid int
	if s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	// Snapshot BEFORE signaling — see the doc comment above.
	children := RunnerChildrenOf(pid)

	select {
	case <-s.exited:
	default:
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-s.exited:
			case <-time.After(ptyGracefulShutdown):
				_ = s.cmd.Process.Kill()
				select {
				case <-s.exited:
				case <-time.After(ptyGracefulShutdown):
					// Best-effort: proceed to the pid sweep below regardless
					// of whether Wait ever observed the exit (e.g. an
					// unreaped zombie) — KillPids targets specific pids by
					// number, not this session's process tree, so it does
					// not depend on s.exited having fired.
				}
			}
		}
	}
	_ = s.pty.Close()

	KillPids(children)
}
