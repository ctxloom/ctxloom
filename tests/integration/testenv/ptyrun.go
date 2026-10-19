package testenv

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ptyGracefulShutdown bounds how long Close waits after SIGTERM before
// escalating to SIGKILL. Generous relative to a mock backend's near-instant
// shutdown, short relative to a test suite: this only pays the cost on the
// (already off the happy path) case of closing a session that didn't exit on
// its own.
const ptyGracefulShutdown = 2 * time.Second

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

	fence     string        // a token expected on the stream, never kept in it
	fenceSeen chan struct{} // closed once fence has arrived (and been cut)
}

func (c *ptyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, _ := c.b.Write(p)
	c.cutFence()
	if c.written != nil {
		close(c.written)
		c.written = nil
	}
	return n, nil
}

// expect arms the capture for token: the write that completes it cuts it out
// of the capture (nobody waiting on a screen ever sees it) and closes the
// returned channel. Caller holds no lock.
func (c *ptyCapture) expect(token string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fence = token
	c.fenceSeen = make(chan struct{})
	seen := c.fenceSeen
	c.cutFence()
	return seen
}

// cutFence removes an armed fence that has fully arrived. Caller holds mu.
func (c *ptyCapture) cutFence() {
	if c.fence == "" {
		return
	}
	cur := c.b.String()
	i := strings.Index(cur, c.fence)
	if i < 0 {
		return
	}
	c.b.Reset()
	c.b.WriteString(cur[:i])
	c.b.WriteString(cur[i+len(c.fence):])
	c.fence = ""
	close(c.fenceSeen)
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
//
// Exit is reported only once the capture holds everything the process wrote
// before it exited (see settle): a process's last line and its exit are two
// events on two goroutines, and Output read after Wait must not depend on
// which of them the scheduler ran first.
type PTYSession struct {
	pty pty.Pty
	cmd *pty.Cmd
	out *ptyCapture
	// cols and rows are the pty's size, the screen Display replays onto.
	cols, rows int

	drained chan struct{} // closed when the drain stops reading the master
	exited  chan struct{} // closed once the process is reaped AND settled
	exitErr error

	hooks ptyHooks
}

// ptyHooks are seams for the harness's own tests, which force the
// interleavings the harness must survive; the zero value is production.
type ptyHooks struct {
	// drainTo wraps the writer the drain copies the master into.
	drainTo func(io.Writer) io.Writer
	// beforeFence runs after the reap, before settle writes its fence.
	beforeFence func(slave *os.File)
	// settling runs once settle has its boundary and waits on it.
	settling func()
}

// ptySettleBound backstops settle: a reaped process's exit is reported after
// at most this long even if the drain neither delivers the fence nor ends.
// Far beyond a settle that works (milliseconds); it only bounds one that
// would otherwise hang the exit until Close.
const ptySettleBound = 5 * time.Second

// ptyFence is what settle writes behind the reaped process's output. No
// newline (ONLCR would rewrite it) and nothing a program prints by accident.
const ptyFence = "\x00<ctxloom-pty-settle-fence>\x00"

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
	env := append(append(e.isolatedEnv(), "TERM=dumb"), extraEnv...)
	var hooks ptyHooks
	if c := e.cast; c != nil {
		// A pty's bytes already carry its own "\r\n": Write, never WritePipe.
		_ = c.Prompt(append([]string{bin}, args...))
		hooks.drainTo = func(w io.Writer) io.Writer { return io.MultiWriter(w, c) }
	}
	return startPTY(hooks, e.ProjectDir, env, bin, cols, rows, args...)
}

// startPTY starts bin in dir with env on a fresh pty, draining the master
// into the session's capture. The pty's own command type is not an
// *exec.Cmd, so it passes the spawn gate by asserting env directly, before
// the pty is even opened.
func startPTY(hooks ptyHooks, dir string, env []string, bin string, cols, rows int, args ...string) (*PTYSession, error) {
	if err := AssertChildEnvSandboxed(env); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	if err := p.Resize(cols, rows); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("resize pty to %dx%d: %w", cols, rows, err)
	}

	cmd := p.CommandContext(context.Background(), bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	s := &PTYSession{
		pty: p, cmd: cmd, out: &ptyCapture{}, cols: cols, rows: rows,
		drained: make(chan struct{}), exited: make(chan struct{}), hooks: hooks,
	}
	var sink io.Writer = s.out
	if hooks.drainTo != nil {
		sink = hooks.drainTo(sink)
	}
	go func() {
		defer close(s.drained)
		_, _ = io.Copy(sink, p)
	}()

	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go func() {
		s.exitErr = cmd.Wait()
		s.settle()
		close(s.exited)
	}()
	return s, nil
}

// settle returns once the drain has delivered everything the reaped process
// wrote to the pty. While a slave stays open — this harness holds one itself,
// as may a process the session left behind — the drain never sees EOF, so the
// boundary is made instead: a fence written to the slave after the reap
// queues behind every byte the process wrote before it, and the capture
// reports the fence when the drain gets to it.
//
// A terminal that refuses the fence has no slave left to write through, and
// then the drain's own end is the boundary: the master reads out what was
// queued and then fails or reads EOF. That is darwin's ordinary case, not an
// edge: when a session leader exits, XNU waits for its output to be read off
// the master and then revokes its controlling terminal, so every slave
// descriptor (this harness's included) fails from then on. It is also the
// case after Hangup or Close, whose closed master has already ended the
// drain. ptySettleBound backstops a drain that neither delivers the fence nor
// ends, so a reaped process's exit is always reported.
func (s *PTYSession) settle() {
	up, ok := s.pty.(pty.UnixPty)
	if !ok {
		return
	}
	seen := s.out.expect(ptyFence)
	if s.hooks.beforeFence != nil {
		s.hooks.beforeFence(up.Slave())
	}
	if _, err := up.Slave().Write([]byte(ptyFence)); err != nil {
		seen = nil // never arrives; wait on the drain alone
	}
	if s.hooks.settling != nil {
		s.hooks.settling()
	}
	select {
	case <-seen:
	case <-s.drained:
	case <-time.After(ptySettleBound):
	}
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

// Output returns everything captured off the pty so far. Once Wait or
// AwaitExit has reported the exit, that includes everything the process
// wrote.
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
// pty delivers, until it holds or testsupport.Expiry(t) fires. It returns the output
// it decided on, so a failure can show the screen it waited for.
func (s *PTYSession) AwaitOutput(t *testing.T, cond func(output string) bool) (string, bool) {
	return s.out.await(testsupport.Expiry(t), cond)
}

// WaitForOutput is AwaitOutput bounded by timeout instead of the test's
// deadline.
func (s *PTYSession) WaitForOutput(timeout time.Duration, cond func(output string) bool) bool {
	_, ok := s.out.await(time.After(timeout), cond)
	return ok
}

// AwaitExit blocks for the process to exit, or until testsupport.Expiry(t) fires.
// exited reports whether it did; err is its Wait error (nil for a clean exit).
func (s *PTYSession) AwaitExit(t *testing.T) (exited bool, err error) {
	return s.waitExit(testsupport.Expiry(t))
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
