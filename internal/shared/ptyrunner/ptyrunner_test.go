//go:build !windows

// Package ptyrunner tests verify PTY-based command execution.
// These tests ensure that interactive commands work correctly through a PTY,
// including proper signal handling and exit code propagation.
//
// NOTE: These tests require Unix-like systems (not Windows) because they
// rely on shell commands and PTY behavior specific to Unix.
package ptyrunner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// syncBuffer is a thread-safe buffer for concurrent read/write in tests.
type syncBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// =============================================================================
// Basic Command Execution Tests
// =============================================================================

// TestRunInteractive_SimpleCommand verifies basic command execution.
// NOTE: We use 'sh -c' with printf+sleep to ensure output is flushed
// before the command exits, which is needed for PTY output capture.
func TestRunInteractive_SimpleCommand(t *testing.T) {
	ctx := context.Background()
	// Use printf with a small sleep to ensure output is flushed
	cmd := exec.Command("sh", "-c", "printf 'hello world\\n'; sleep 0.1")

	var stdout bytes.Buffer
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, &stdout, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	// Output may include terminal control characters, so just check content
	assert.Contains(t, stdout.String(), "hello world")
}

// TestRunInteractive_ExitCode verifies exit codes are properly captured.
func TestRunInteractive_ExitCode(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		args         []string
		expectedCode int
	}{
		{
			name:         "success",
			command:      "sh",
			args:         []string{"-c", "exit 0"},
			expectedCode: 0,
		},
		{
			name:         "failure",
			command:      "sh",
			args:         []string{"-c", "exit 1"},
			expectedCode: 1,
		},
		{
			name:         "custom exit code",
			command:      "sh",
			args:         []string{"-c", "exit 42"},
			expectedCode: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cmd := exec.Command(tt.command, tt.args...)

			exitCode, err := RunInteractive(ctx, cmd, nil, nil, nil, nil)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedCode, exitCode)
		})
	}
}

// =============================================================================
// Context Cancellation Tests
// =============================================================================
// These tests verify that context cancellation properly terminates child processes.

// TestRunInteractive_ContextCancellation verifies that cancelling the context
// terminates the child process.
func TestRunInteractive_ContextCancellation(t *testing.T) {
	// Create a context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Run a command that would normally run for a long time
	cmd := exec.Command("sh", "-c", "sleep 30")

	// Start the command in a goroutine
	resultCh := make(chan struct {
		exitCode int
		err      error
	})
	go func() {
		exitCode, err := RunInteractive(ctx, cmd, nil, nil, nil, nil)
		resultCh <- struct {
			exitCode int
			err      error
		}{exitCode, err}
	}()

	// Give the command time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel the context
	cancel()

	// Wait for the result with a timeout
	select {
	case res := <-resultCh:
		// The command should have been terminated
		// Exit code may be non-zero (killed by signal) or the command may return an error
		if res.err == nil {
			// If no error, exit code should be non-zero (signal termination)
			assert.NotEqual(t, 0, res.exitCode, "cancelled command should have non-zero exit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not terminate after context cancellation")
	}
}

// TestRunInteractive_ContextTimeout verifies that context timeouts work.
func TestRunInteractive_ContextTimeout(t *testing.T) {
	// Create a context with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Run a command that would normally run longer than the timeout
	cmd := exec.Command("sh", "-c", "sleep 30")

	start := time.Now()
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, nil, nil)
	elapsed := time.Since(start)

	// Should complete quickly (within ~500ms, not 30 seconds)
	assert.Less(t, elapsed, 2*time.Second, "command should be terminated by timeout")

	// Command may return error or non-zero exit code
	if err == nil {
		assert.NotEqual(t, 0, exitCode, "timed-out command should have non-zero exit")
	}
}

// =============================================================================
// Initial-Size Tests (garbled paint until manual resize)
// =============================================================================

// TestRunInteractive_SizesPTYBeforeChildStarts pins the root cause:
// go-pty allocates the pty at its own default winsize, and the pre-fix code
// applied resize events ONLY via a goroutine started AFTER Start() — a chase
// that can never catch a child whose first paint runs before the size
// arrives. In production the real size travels a whole chain of goroutine
// hops plus a gRPC round trip before it reaches here (see run.go's
// interactiveTerminal → termui.Controller → ResizeTranslator →
// goplugin.Launcher → the wire → this package); a single in-process channel
// send is far faster than that, so a same-process test has to reintroduce a
// comparable delay to exercise the real race — otherwise the pre-start
// goroutine always "wins" trivially and the test can't tell the two
// implementations apart. `stty size` (no sleep) is the fast child: it reads
// its controlling terminal's size and exits immediately, well inside the
// delay window, so this only passes if the pty was already sized BEFORE
// Start — not chased in afterward.
func TestRunInteractive_SizesPTYBeforeChildStarts(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sh", "-c", "stty size")

	const wireDelay = 50 * time.Millisecond
	resize := make(chan agent.WindowSize, 1)
	go func() {
		time.Sleep(wireDelay)
		resize <- agent.WindowSize{Rows: 55, Cols: 111} // an arbitrary, non-default size
	}()

	var stdout bytes.Buffer
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, &stdout, resize)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Contains(t, strings.TrimSpace(stdout.String()), "55 111",
		"the pty must already report the frontend's real size (55 111) at the child's "+
			"first read, not go-pty's default winsize — RunInteractive must wait for the "+
			"first resize value and apply it before Start, not race the child against it")
}

// =============================================================================
// Signal Handling Tests (via PTY)
// =============================================================================
// These tests verify signal-related behavior in PTY mode.

// TestRunInteractive_SignalThroughPTY verifies that signals sent to the child
// process through the PTY work correctly.
// When running in a PTY, signals like SIGINT are sent by writing the
// corresponding control character to the PTY (Ctrl+C = 0x03).
func TestRunInteractive_SignalThroughPTY(t *testing.T) {
	ctx := context.Background()

	// Run a shell that will echo "trapped" when it receives SIGINT
	// and then exit with code 130 (128 + signal number)
	cmd := exec.Command("sh", "-c", `
		trap 'echo trapped; exit 130' INT
		echo ready
		sleep 30
	`)

	var stdout syncBuffer
	resultCh := make(chan struct {
		exitCode int
		err      error
	})
	go func() {
		exitCode, err := RunInteractive(ctx, cmd, nil, nil, &stdout, nil)
		resultCh <- struct {
			exitCode int
			err      error
		}{exitCode, err}
	}()

	// Wait for "ready" to appear in output (command has started)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "ready") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Contains(t, stdout.String(), "ready", "command should have started")

	// At this point the command is running and will be killed by context timeout
	// We're verifying it started and can be controlled

	// Wait for result (command will be killed by our test timeout)
	select {
	case <-resultCh:
		// Command finished (may have been killed)
	case <-time.After(3 * time.Second):
		// This is acceptable - we've verified the command started
	}
}

// =============================================================================
// Output Capture Tests
// =============================================================================

// TestRunInteractive_CapturesOutput verifies stdout is captured correctly.
func TestRunInteractive_CapturesOutput(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sh", "-c", "echo line1; echo line2; echo line3")

	var stdout bytes.Buffer
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, &stdout, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	// Output is delivered ONLY through the caller's writer; the runner keeps
	// no in-memory copy of the session stream.
	assert.Contains(t, stdout.String(), "line1")
	assert.Contains(t, stdout.String(), "line2")
	assert.Contains(t, stdout.String(), "line3")
}

// failingWriter fails every Write after the first n bytes (n==0 fails
// immediately), simulating a destination that goes away mid-stream (a gRPC
// stream on a broken pipe/connection reset).
type failingWriter struct {
	n     int
	err   error
	wrote int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.wrote >= w.n {
		return 0, w.err
	}
	take := len(p)
	if w.wrote+take > w.n {
		take = w.n - w.wrote
	}
	w.wrote += take
	if take < len(p) {
		return take, w.err
	}
	return take, nil
}

// TestRunInteractive_StdoutWriteFailureReportsError pins the fix: a write
// failure delivering the child's PTY output used to be discarded
// (`_, _ = io.Copy(dst, ptty)`), so RunInteractive reported the child's exit
// code (often 0) as success even though delivery failed partway through.
func TestRunInteractive_StdoutWriteFailureReportsError(t *testing.T) {
	ctx := context.Background()
	// A child that writes enough to guarantee at least one PTY read/write
	// cycle happens before it exits.
	cmd := exec.Command("sh", "-c", "printf 'hello world\\n'; sleep 0.1")

	dst := &failingWriter{n: 0, err: errors.New("simulated broken pipe")}
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, dst, nil)

	require.Error(t, err, "a stdout delivery failure must not be reported as success")
	assert.Contains(t, err.Error(), "output delivery failed")
	assert.Contains(t, err.Error(), "simulated broken pipe")
	assert.Zero(t, exitCode, "the zero-value exit code on an error return must never be mistaken for a real 0 (success) exit")
}

// TestRunInteractive_ClosesPipeReaderWhenCopierExits is the regression for the
// wedged-stream-pump bug: the wire stdin is an io.Pipe, and a Write into a pipe
// nobody reads parks forever — once the stdin copier stopped reading without
// closing the reader, the server's stream pump blocked on its next stdin
// forward and never drained another resize. The copier must close the
// *io.PipeReader on exit so pending/future writes fail with ErrClosedPipe.
func TestRunInteractive_ClosesPipeReaderWhenCopierExits(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sh", "-c", "sleep 0.1")

	stdinR, stdinW := io.Pipe()
	exitCode, err := RunInteractive(ctx, cmd, stdinR, func() { _ = stdinR.Close() }, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	// The run is over; the copier may still be parked in stdinR.Read. The
	// first write below is consumed by that final Read (after which the copier
	// observes done and exits, closing the reader); every later write must
	// fail fast with ErrClosedPipe instead of parking the writer forever.
	errCh := make(chan error, 1)
	go func() {
		for {
			if _, werr := stdinW.Write([]byte("x")); werr != nil {
				errCh <- werr
				return
			}
		}
	}()
	select {
	case werr := <-errCh:
		assert.ErrorIs(t, werr, io.ErrClosedPipe)
	case <-time.After(5 * time.Second):
		t.Fatal("stdinW.Write still blocked after the run ended; the stdin reader was not closed")
	}
}

// wrappedStdin hides a reader's concrete type behind a plain io.Reader, the
// way coord's terminal-wake nudgeReader does in production once llm_serve.go
// arms wrapStreams. It exists to deny RunInteractive the one thing the old
// cleanup depended on — that stdin IS an *io.PipeReader — without dragging a
// dependency on internal/agentcoord into the substrate.
type wrappedStdin struct{ r io.Reader }

func (w wrappedStdin) Read(p []byte) (int, error) { return w.r.Read(p) }

// TestRunInteractive_ClosesWrappedStdinWhenCopierExits is the sibling of
// TestRunInteractive_ClosesPipeReaderWhenCopierExits for the case that
// actually ships. The wedge it guards is identical — a Write into a pipe
// nobody reads parks forever, and context cancellation does not unblock it —
// but the reader handed to RunInteractive is WRAPPED, so the type assertion
// the cleanup used to be gated on cannot hold and the close silently never
// ran. The caller that created the pipe supplies the cleanup explicitly, so
// there is no longer any reader identity for RunInteractive to guess at.
func TestRunInteractive_ClosesWrappedStdinWhenCopierExits(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sh", "-c", "sleep 0.1")

	stdinR, stdinW := io.Pipe()
	exitCode, err := RunInteractive(ctx, cmd, wrappedStdin{stdinR}, func() { _ = stdinR.Close() }, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	// Same probe as the *io.PipeReader case: the first write is consumed by
	// the copier's final parked Read, and every later write must fail fast
	// with ErrClosedPipe rather than parking the server's stream pump forever.
	errCh := make(chan error, 1)
	go func() {
		for {
			if _, werr := stdinW.Write([]byte("x")); werr != nil {
				errCh <- werr
				return
			}
		}
	}()
	select {
	case werr := <-errCh:
		assert.ErrorIs(t, werr, io.ErrClosedPipe)
	case <-time.After(5 * time.Second):
		t.Fatal("stdinW.Write still blocked after the run ended: the cleanup for a wrapped stdin never ran, " +
			"so the server's stream pump wedges and drops every later resize")
	}
}

// TestRunInteractive_ReleasesStdinWhenCopierStopsNotWhenRunEnds pins the
// reason the stdin copier carries its OWN release in addition to
// RunInteractive's deferred one. The two are not redundant: RunInteractive's
// defer cannot fire until the child exits, so for a session whose stdin ends
// early — the wire half-closing while a long interactive turn keeps running —
// only the copier's release lets the writer side learn that nobody is reading
// anymore. Without it the wire writer stays parked for the rest of the run.
//
// The child deliberately outlives stdin by a wide margin, so "released when
// the copier stopped" and "released when the run ended" are far enough apart
// that the assertion cannot pass by coincidence.
func TestRunInteractive_ReleasesStdinWhenCopierStopsNotWhenRunEnds(t *testing.T) {
	released := make(chan struct{})
	// Ends at once (EOF on first Read), wrapped so no type assertion can see it.
	stdin := wrappedStdin{strings.NewReader("")}
	cmd := exec.Command("sh", "-c", "sleep 3")

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = RunInteractive(context.Background(), cmd, stdin, func() { close(released) }, nil, nil)
	}()

	select {
	case <-released:
		// Fixture guard: the run must still be going, or this proves nothing
		// about WHEN the release happened.
		select {
		case <-runDone:
			t.Fatal("the run finished before the release was observed; this test can no longer tell " +
				"the copier's release apart from RunInteractive's deferred one")
		default:
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stdin was not released when the copier stopped reading: a wire writer would stay " +
			"parked for the rest of the run")
	}
	<-runDone
}

// TestRunInteractive_ChildStderrArrivesOnStdoutWriter pins the invariant that
// makes a separate stderr writer meaningless here: a pty gives the child ONE
// stream. The child's fd 1 and fd 2 are both the pty slave, so the master
// hands back a single interleaved byte stream and there is no separation left
// to route anywhere. Any caller wanting split streams must not use a pty at
// all (see internal/lm/backends' non-interactive branch, which wires
// cmd.Stdout/cmd.Stderr directly). This is a characterization pin, green
// before and after the parameter's removal — its job is to keep the claim the
// signature now makes ("output" not "stdout") true if the copy path is ever
// reworked.
func TestRunInteractive_ChildStderrArrivesOnStdoutWriter(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sh", "-c", "printf 'to-stdout\\n'; printf 'to-stderr\\n' 1>&2; sleep 0.1")

	var out bytes.Buffer
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, &out, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Contains(t, out.String(), "to-stdout")
	assert.Contains(t, out.String(), "to-stderr",
		"a pty merges the child's fd 2 into the single master stream; the caller's "+
			"one writer receives both, which is why a second writer cannot be honoured")
}

// TestRunInteractive_HandBuiltCmdWithoutArgsRuns pins the nil-Args crash:
// os/exec defaults an empty Args to a one-element slice containing Path, but
// it does that inside exec.Cmd.Start, which RunInteractive never calls — it
// re-derives the argv for go-pty's own Cmd instead. So a hand-built
// &exec.Cmd{Path: ...} (legal, and the shape any caller assembling a spec
// rather than calling exec.Command produces) still carries a nil Args when it
// arrives here, and slicing it at [1:] panics.
func TestRunInteractive_HandBuiltCmdWithoutArgsRuns(t *testing.T) {
	truePath, err := exec.LookPath("true")
	require.NoError(t, err)

	// Args deliberately left nil — that is the whole fixture.
	cmd := &exec.Cmd{Path: truePath}
	require.Nil(t, cmd.Args, "the fixture is only hostile while Args is nil")

	var exitCode int
	var runErr error
	require.NotPanics(t, func() {
		exitCode, runErr = RunInteractive(context.Background(), cmd, nil, nil, nil, nil)
	}, "a *exec.Cmd with no Args must not panic the runner")
	require.NoError(t, runErr)
	assert.Equal(t, 0, exitCode)
}

// TestRunInteractive_Argv0ComesFromPath characterizes a limitation of the
// underlying pty library rather than a choice made here: go-pty's Cmd.start
// rebuilds the child's argv as exec.Command(c.Path, c.Args[1:]...), so argv[0]
// is ALWAYS the resolved Path and a caller's own Args[0] can never survive.
// exec.Command itself sets Args[0] to the name as written ("sh"), so the two
// already differ for every ordinary caller. Pinned so that a future change of
// pty library — the only way to honour a custom argv[0] — is a deliberate,
// visible decision rather than a silent behaviour change.
func TestRunInteractive_Argv0ComesFromPath(t *testing.T) {
	cmd := exec.Command("sh", "-c", `printf 'argv0=%s\n' "$0"; sleep 0.1`)
	require.Equal(t, "sh", cmd.Args[0], "exec.Command sets argv[0] to the name as written")
	require.NotEqual(t, cmd.Args[0], cmd.Path, "the fixture needs Path and Args[0] to differ")

	var out bytes.Buffer
	exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, &out, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Contains(t, out.String(), "argv0="+cmd.Path,
		"go-pty derives argv[0] from Path; the caller's Args[0] is not reachable from here")
}

// stubResize replaces the package's pty-resize call for the duration of the
// test. A live pty's TIOCSWINSZ cannot be made to fail on demand, so the seam
// is the only way to exercise the error handling at all — which is exactly why
// both errors went unhandled for so long.
func stubResize(t *testing.T, fn func(ws agent.WindowSize) error) {
	t.Helper()
	orig := resizePTY
	resizePTY = func(_ pty.Pty, ws agent.WindowSize) error { return fn(ws) }
	t.Cleanup(func() { resizePTY = orig })
}

// TestRunInteractive_InitialResizeFailureIsReported pins the more serious half
// of the discarded pair. The pre-start resize is the entire mechanism behind
// that fix: the child must see its real geometry before its
// first paint, and because SIGWINCH fires only on a CHANGE, a size that never
// lands never self-heals. Swallowing that error started a child guaranteed to
// paint wrong for the whole session and reported success.
func TestRunInteractive_InitialResizeFailureIsReported(t *testing.T) {
	stubResize(t, func(agent.WindowSize) error { return errors.New("simulated TIOCSWINSZ failure") })

	resize := make(chan agent.WindowSize, 1)
	resize <- agent.WindowSize{Rows: 55, Cols: 111}

	cmd := exec.Command("sh", "-c", "printf 'child ran\\n'; sleep 0.1")
	var out bytes.Buffer
	exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, &out, resize)

	require.Error(t, err, "a failed pre-start resize must not be reported as a successful run")
	assert.Contains(t, err.Error(), "failed to size pty before starting the child")
	assert.Contains(t, err.Error(), "simulated TIOCSWINSZ failure")
	assert.Zero(t, exitCode)
	assert.NotContains(t, out.String(), "child ran",
		"the child must not be started at a geometry known to be wrong")
}

// TestRunInteractive_LaterResizeFailureIsReported pins the second discarded
// error: a SIGWINCH that stops reaching the pty mid-session. The first resize
// succeeds (so the child starts normally); the next one fails, which used to
// be discarded inside a loop that then went on retrying in silence.
func TestRunInteractive_LaterResizeFailureIsReported(t *testing.T) {
	var calls atomic.Int32
	stubResize(t, func(agent.WindowSize) error {
		if calls.Add(1) == 1 {
			return nil // the pre-start resize succeeds
		}
		return errors.New("simulated mid-session TIOCSWINSZ failure")
	})

	// Both sizes are queued up front: the pre-start select consumes the first,
	// the applier goroutine consumes the second immediately after Start.
	resize := make(chan agent.WindowSize, 2)
	resize <- agent.WindowSize{Rows: 55, Cols: 111}
	resize <- agent.WindowSize{Rows: 24, Cols: 80}

	cmd := exec.Command("sh", "-c", "sleep 0.3")
	exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, nil, resize)

	require.Error(t, err, "a resize that stopped reaching the pty must not be reported as success")
	assert.Contains(t, err.Error(), "terminal resize failed")
	assert.Contains(t, err.Error(), "simulated mid-session TIOCSWINSZ failure")
	assert.Zero(t, exitCode)
	assert.GreaterOrEqual(t, calls.Load(), int32(2), "the applier goroutine must have run")
}

// TestRunInteractive_LateResizeOnClosedPTYIsNotAnError guards the fix against
// its own false positive. RunInteractive closes the pty before the deferred
// close(done) stops the resize applier, so a queued event can legitimately
// land on a closed master at end of run. That is the same expected fallout
// isBenignPTYError already names for c.Wait, and reporting it would turn every
// well-behaved session into a failure.
func TestRunInteractive_LateResizeOnClosedPTYIsNotAnError(t *testing.T) {
	var calls atomic.Int32
	stubResize(t, func(agent.WindowSize) error {
		if calls.Add(1) == 1 {
			return nil
		}
		return fs.ErrClosed
	})

	resize := make(chan agent.WindowSize, 2)
	resize <- agent.WindowSize{Rows: 55, Cols: 111}
	resize <- agent.WindowSize{Rows: 24, Cols: 80}

	cmd := exec.Command("sh", "-c", "sleep 0.3")
	exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, nil, resize)

	require.NoError(t, err, "our own close racing a queued resize is expected fallout, not a defect")
	assert.Equal(t, 0, exitCode)
	assert.GreaterOrEqual(t, calls.Load(), int32(2), "the applier goroutine must have run")
}

// stubClose replaces the package's pty-close call. The stub must still close
// the real master: the output copier is parked in a read on it and only the
// close unblocks it, so a stub that merely records would hang the run.
func stubClose(t *testing.T, fn func(ptty pty.Pty) error) {
	t.Helper()
	orig := closePTY
	closePTY = fn
	t.Cleanup(func() { closePTY = orig })
}

// TestRunInteractive_ClosesPTYExactlyOnce pins the invariant that replaces a
// borrowed one. Close used to be called twice on every normal run — once
// explicitly to unblock the copier, once by the deferred cleanup — with both
// results discarded, so correctness rested on go-pty's Close being idempotent.
// Its API does not promise that; it holds today only because the Unix
// implementation closes *os.File handles that track their own closed state.
func TestRunInteractive_ClosesPTYExactlyOnce(t *testing.T) {
	var closes atomic.Int32
	stubClose(t, func(ptty pty.Pty) error {
		closes.Add(1)
		return ptty.Close()
	})

	cmd := exec.Command("sh", "-c", "printf 'hi\\n'; sleep 0.1")
	var out bytes.Buffer
	exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, &out, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, int32(1), closes.Load(), "the pty master must be closed exactly once per run")
}

// TestRunInteractive_NonBenignCloseFailureIsReported pins the other half: with
// one call there is one result, and it is no longer dropped. Closing a pty
// whose child has already exited yields expected fallout (isBenignPTYError),
// which must stay silent; anything else is a real failure of the run.
func TestRunInteractive_NonBenignCloseFailureIsReported(t *testing.T) {
	t.Run("benign fallout stays silent", func(t *testing.T) {
		stubClose(t, func(ptty pty.Pty) error {
			_ = ptty.Close() // still unblock the copier
			return fs.ErrClosed
		})
		cmd := exec.Command("sh", "-c", "printf 'hi\\n'; sleep 0.1")
		exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, exitCode)
	})

	t.Run("a real close failure is reported", func(t *testing.T) {
		stubClose(t, func(ptty pty.Pty) error {
			_ = ptty.Close()
			return errors.New("simulated close failure")
		})
		cmd := exec.Command("sh", "-c", "printf 'hi\\n'; sleep 0.1")
		exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pty close failed")
		assert.Contains(t, err.Error(), "simulated close failure")
		assert.Zero(t, exitCode)
	})
}

// TestRunInteractive_SignalKilledChildYields128PlusSignum pins the POSIX
// convention for a child that died on a signal. It replaces an earlier pin
// that asserted the raw -1 os/exec reports for a signalled process: passing
// that through meant internal/cli's ExitError carried -1 into os.Exit and the
// OS truncated it to 255, which is indistinguishable both from an engine that
// really exited 255 and from a runner-internal failure. -1 is not a valid
// POSIX exit status at all. `ctxloom run` is a transparent wrapper around the
// engine's status, so the signal case now reports what a shell reports:
// 128+signum. Changing this back is a user-visible exit-code change and has to
// be taken deliberately, by someone who has to edit this test.
func TestRunInteractive_SignalKilledChildYields128PlusSignum(t *testing.T) {
	tests := []struct {
		name         string
		signal       string
		expectedCode int
	}{
		{name: "SIGINT", signal: "INT", expectedCode: 130},
		{name: "SIGKILL", signal: "KILL", expectedCode: 137},
		{name: "SIGTERM", signal: "TERM", expectedCode: 143},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "kill -"+tt.signal+" $$; sleep 5")
			exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedCode, exitCode,
				"a child killed by SIG%s must report 128+signum, not the raw -1 os/exec hands back", tt.signal)
		})
	}
}
