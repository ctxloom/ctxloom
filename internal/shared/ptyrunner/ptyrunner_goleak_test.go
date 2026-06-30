//go:build !windows

package ptyrunner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestRunInteractive_StdinGoroutineDoesNotLeak pins subarctic-backed-garnet: the
// stdin-copy goroutine is a *bounded* park (it cannot outlive the next stdin
// event), not an unbounded leak. We prove it with goroutine-leak detection.
//
// RunInteractive reads the process-global os.Stdin, so we swap in the read end
// of a pipe we control. After the command exits and the PTY is closed, the
// goroutine is parked in os.Stdin.Read; closing the WRITE end delivers EOF —
// "the next input event" — which must unpark and terminate it. goleak then
// confirms no goroutine started during RunInteractive survives.
//
// We deliver EOF by closing the write end (w), never the read end (r=os.Stdin):
// RunInteractive's resize goroutine reads os.Stdin.Fd() and is signalled-but-not
// -joined, so closing r from the test would race it under -race. Closing w
// touches a different file object and is race-free.
func TestRunInteractive_StdinGoroutineDoesNotLeak(t *testing.T) {
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdin = r
	// Restore only after VerifyNone has confirmed the goroutines are gone, so
	// the global-pointer write can't race their reads. r is intentionally left
	// open (a short-lived fd in the test process) to avoid racing the unjoined
	// resize goroutine's os.Stdin.Fd() read.
	t.Cleanup(func() { os.Stdin = oldStdin })

	// Baseline AFTER the swap but BEFORE RunInteractive, so the stdin-copy and
	// resize goroutines spawned by RunInteractive are in scope for the check.
	ignore := goleak.IgnoreCurrent()

	// The trailing sleep keeps the command alive briefly so the PTY-output
	// copy goroutine drains before ptty.Close() (matches the idiom in
	// TestRunInteractive_SimpleCommand); without it a fast exit can race the
	// drain and drop output in some environments.
	cmd := exec.Command("sh", "-c", "printf 'hello from pty\\n'; sleep 0.1")
	var out bytes.Buffer
	result, err := RunInteractive(context.Background(), cmd, nil, &out, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, out.String(), "hello from pty")

	// Deliver the next input event: EOF on stdin. This is what must unpark the
	// goroutine still blocked in os.Stdin.Read.
	require.NoError(t, w.Close())

	// goleak retries with backoff; the parked goroutine must exit after the EOF.
	goleak.VerifyNone(t, ignore)
}

// TestRunInteractive_BenignPTYCloseSwallowed confirms the errors.Is-based
// benign-error handling (also subarctic-backed-garnet): closing the PTY after
// the command exits produces fs.ErrClosed / syscall.EIO fallout that must be
// swallowed, so a normal interactive run returns cleanly rather than surfacing
// a spurious "command failed". No os.Stdin swap here — under `go test` the
// stdin goroutine reads the real (EOF-ing) stdin and exits on its own.
func TestRunInteractive_BenignPTYCloseSwallowed(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'line one\\nline two\\n'; sleep 0.1")
	var out bytes.Buffer
	result, err := RunInteractive(context.Background(), cmd, nil, &out, nil, nil)
	require.NoError(t, err, "benign PTY-close fallout must be swallowed via errors.Is")
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, out.String(), "line one")
	assert.Contains(t, out.String(), "line two")
}
