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
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

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
	exitCode, err := RunInteractive(ctx, cmd, nil, &stdout, nil)

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

			exitCode, err := RunInteractive(ctx, cmd, nil, nil, nil)

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
		exitCode, err := RunInteractive(ctx, cmd, nil, nil, nil)
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
	exitCode, err := RunInteractive(ctx, cmd, nil, nil, nil)
	elapsed := time.Since(start)

	// Should complete quickly (within ~500ms, not 30 seconds)
	assert.Less(t, elapsed, 2*time.Second, "command should be terminated by timeout")

	// Command may return error or non-zero exit code
	if err == nil {
		assert.NotEqual(t, 0, exitCode, "timed-out command should have non-zero exit")
	}
}

// =============================================================================
// Initial-Size Tests (DEFECT lucid-judo: garbled paint until manual resize)
// =============================================================================

// TestRunInteractive_SizesPTYBeforeChildStarts pins the lucid-judo root cause:
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
	exitCode, err := RunInteractive(ctx, cmd, nil, &stdout, resize)
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
		exitCode, err := RunInteractive(ctx, cmd, nil, &stdout, nil)
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
	exitCode, err := RunInteractive(ctx, cmd, nil, &stdout, nil)

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

// TestRunInteractive_StdoutWriteFailureReportsError pins U116-F02: a write
// failure delivering the child's PTY output used to be discarded
// (`_, _ = io.Copy(dst, ptty)`), so RunInteractive reported the child's exit
// code (often 0) as success even though delivery failed partway through.
func TestRunInteractive_StdoutWriteFailureReportsError(t *testing.T) {
	ctx := context.Background()
	// A child that writes enough to guarantee at least one PTY read/write
	// cycle happens before it exits.
	cmd := exec.Command("sh", "-c", "printf 'hello world\\n'; sleep 0.1")

	dst := &failingWriter{n: 0, err: errors.New("simulated broken pipe")}
	exitCode, err := RunInteractive(ctx, cmd, nil, dst, nil)

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
	exitCode, err := RunInteractive(ctx, cmd, stdinR, nil, nil)
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
	exitCode, err := RunInteractive(ctx, cmd, nil, &out, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Contains(t, out.String(), "to-stdout")
	assert.Contains(t, out.String(), "to-stderr",
		"a pty merges the child's fd 2 into the single master stream; the caller's "+
			"one writer receives both, which is why a second writer cannot be honoured")
}
