package testenv

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

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
// directory.
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
func (e *TestEnvironment) RunPTY(cols, rows int, args ...string) (*PTYSession, error) {
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
	cmd.Env = append(e.isolatedEnv(), "TERM=dumb")

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

// Resize changes the pty's window size, delivering SIGWINCH to the child.
func (s *PTYSession) Resize(cols, rows int) error { return s.pty.Resize(cols, rows) }

// Output returns everything captured off the pty so far.
func (s *PTYSession) Output() string { return s.out.String() }

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
// assertion failed before the session exited on its own), kills it so a
// failed test never leaks a child process. Safe to call after a natural exit.
func (s *PTYSession) Close() {
	select {
	case <-s.exited:
	default:
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	}
	_ = s.pty.Close()
}
