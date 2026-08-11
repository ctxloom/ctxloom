package testenv

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// ptyGracefulShutdown bounds how long Close waits after SIGTERM before
// escalating to SIGKILL. Generous relative to a mock backend's near-instant
// shutdown, short relative to a test suite: this only pays the cost on the
// (already off the happy path) case of closing a session that didn't exit on
// its own.
const ptyGracefulShutdown = 2 * time.Second

// pollInterval is PTYSession.WaitForOutput's deadline-poll tick — short enough
// that an assertion lands promptly once the condition is true, long enough not
// to spin a CPU while waiting.
const pollInterval = time.Millisecond

// ptyCapture is a goroutine-safe accumulator for everything read off a pty's
// master side: the pty's own io.Copy-draining goroutine writes into it while
// the test reads it back at will. Same idiom as internal/termui's
// overlay_composition_test.go syncBuf / internal/cli/tui's overlay_test.go
// syncBuffer — re-derived here (rather than exported and reused) because this
// one backs a real subprocess's pty, not an in-process pty pair, and those
// types are unexported to a different package besides.
type ptyCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (c *ptyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}

func (c *ptyCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// PTYSession is a live ctxloom subprocess attached to a real pty. Unlike Run/
// RunWithStdin (which run a command to completion and capture the result), a
// PTYSession stays around so the caller can write keystrokes into it —
// including the Ctrl-] viewer prefix (0x1d) — and deadline-poll the
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
// sequences, which internal/termui writes unconditionally and never gates on
// TERM.
func (e *TestEnvironment) RunPTY(cols, rows int, extraEnv []string, args ...string) (*PTYSession, error) {
	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	if err := p.Resize(cols, rows); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("resize pty to %dx%d: %w", cols, rows, err)
	}

	cmd := p.CommandContext(context.Background(), e.AppBinary, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = append(append(e.isolatedEnv(), "TERM=dumb"), extraEnv...)
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	s := &PTYSession{pty: p, cmd: cmd, out: &ptyCapture{}, exited: make(chan struct{})}
	go func() { _, _ = io.Copy(s.out, p) }()

	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("start %s: %w", e.AppBinary, err)
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

// Output returns everything captured off the pty so far.
func (s *PTYSession) Output() string { return s.out.String() }

// PID returns the session's top-level ctxloom process id, valid once RunPTY
// has returned successfully (cmd.Start already ran). Callers that need to
// find the process's OWN children (e.g. testenv.PluginChildrenOf, to observe
// a spawned plugin subprocess) need this — cmd itself is unexported so
// nothing outside this file can reach *os.Process directly.
func (s *PTYSession) PID() int {
	if s.cmd.Process == nil {
		return -1
	}
	return s.cmd.Process.Pid
}

// WaitForOutput deadline-polls (a bounded, short-interval poll — never a bare
// sleep as the synchronization primitive itself) until cond reports true
// against the accumulated output, or timeout elapses without it.
func (s *PTYSession) WaitForOutput(timeout time.Duration, cond func(output string) bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond(s.out.String()) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// Wait blocks for the process to exit, up to timeout. exited reports whether
// it did before the deadline; err is its Wait error (nil for a clean exit).
func (s *PTYSession) Wait(timeout time.Duration) (exited bool, err error) {
	select {
	case <-s.exited:
		return true, s.exitErr
	case <-time.After(timeout):
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
// failed test never leaks a child process — including the "llm serve
// <label>" plugin subprocess ctxloom self-execs for the run (internal/lm/
// grpc's NewSelfInvokingClientForLabelEnv, setsid'd into its own session so
// it survives outside this session's process group).
//
// Graceful first: SIGTERM is exactly what cmd/run.go's own
// signal.NotifyContext(shutdownSignals) already listens for, unwinding
// through its `defer client.Kill()` — the plugin subprocess's designed
// shutdown path (killSession), which this harness gets for free by using
// it. A bare SIGKILL (the previous, only, behavior here) bypasses that
// entirely: the parent never gets a chance to run its own cleanup, and the
// plugin child — in a different session by construction — is not reachable
// by anything this harness could signal as a group. That gap is exactly how
// this defect accumulated 208 orphaned "llm serve mock" processes over 8
// days on this box (pgrep -fc, all ppid=1, 15/18 distinct binary paths
// already deleted): every PTY-driven test whose assertion failed before the
// session exited on its own was hard-killing its ctxloom process and
// silently leaving the mock behind.
//
// SIGKILL remains the fallback for a process that doesn't honor SIGTERM
// within ptyGracefulShutdown. Either way, Close finishes by explicitly
// reaping any plugin child captured before the signal was sent — the pid,
// not a re-derived one, since a dead parent's former child is reparented to
// init (ppid 1) within the same instant and can no longer be found by
// ppid — so a SIGKILL'd or simply slow parent never leaves the mock behind
// even if its own graceful path didn't get there first. Safe to call after a
// natural exit.
func (s *PTYSession) Close() {
	var pid int
	if s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	// Snapshot BEFORE signaling — see the doc comment above.
	children := PluginChildrenOf(pid)

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
