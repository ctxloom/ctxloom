//go:build !windows

package ptyrunner

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestRunInteractive_StdinGoroutineDoesNotLeak pins subarctic-backed-garnet:
// neither goroutine RunInteractive starts may outlive it.
//
// Two goroutines are in scope, and BOTH must actually be started for the check
// to mean anything — that is the whole content of this pin:
//
//   - the stdin copier, started only when stdin != nil. It parks in
//     stdin.Read, which cannot observe close(done); running the caller's
//     stdinCleanup is the only thing that unparks it.
//   - the resize applier, started only when resize != nil. It selects on done.
//
// Passing nil for either argument means the corresponding goroutine is never
// created, and goleak then confirms nothing about it — a leak check over
// goroutines that were never spawned is green by construction. So this test
// supplies a live *io.PipeReader and a live resize channel, and requires the
// child to have observed both (it reports the applied window size and echoes
// the line written on stdin) BEFORE VerifyNone runs. Those two requires are
// the fixture guard: without them a change that stopped starting either
// goroutine would leave this test passing while pinning nothing.
//
// RunInteractive never reads the process-global os.Stdin — stdin and the
// window size are both injected — so no os.Stdin swap belongs here.
func TestRunInteractive_StdinGoroutineDoesNotLeak(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	resize := make(chan agent.WindowSize, 1)
	resize <- agent.WindowSize{Rows: 55, Cols: 111} // arbitrary, non-default size

	// The write blocks until the copier reads it; the child echoes what it got.
	go func() { _, _ = stdinW.Write([]byte("ping\n")) }()

	// Baseline taken immediately before the call, so both goroutines
	// RunInteractive starts are in scope for VerifyNone (see §11c: without
	// IgnoreCurrent the check counts goroutines other tests already started).
	ignore := goleak.IgnoreCurrent()

	// `stty size` proves the resize applier ran; `read` proves the stdin copier
	// delivered a byte into the pty. The trailing sleep keeps the child alive
	// briefly so the pty-output copier drains before the forced close (the
	// idiom in TestRunInteractive_SimpleCommand).
	cmd := exec.Command("sh", "-c", "stty size; read line; printf 'got %s\\n' \"$line\"; sleep 0.1")
	var out bytes.Buffer
	exitCode, err := RunInteractive(context.Background(), cmd, stdinR, func() { _ = stdinR.Close() }, &out, resize)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	// Fixture guard: both goroutines demonstrably did their work. If either of
	// these fails, the goroutine under test was never started and the leak
	// check below would be vacuous.
	require.Contains(t, out.String(), "55 111",
		"the resize applier must have started and delivered the window size")
	require.Contains(t, out.String(), "got ping",
		"the stdin copier must have started and delivered stdin into the pty")

	// The copier may still be parked in stdinR.Read. Running the cleanup this
	// caller supplied is what must unpark it; the test deliberately does
	// nothing to help, which is precisely the property being pinned.
	goleak.VerifyNone(t, ignore)

	// Only now tidy the pipe's write end, so it can never have been the thing
	// that unparked the copier.
	_ = stdinW.Close()
}

// TestRunInteractive_WrappedStdinGoroutineDoesNotLeak is the leak half of the
// same defect TestRunInteractive_ClosesWrappedStdinWhenCopierExits pins on the
// wedge side. The sibling test above supplies a bare *io.PipeReader, which is
// the one shape the old type-asserted cleanup could still see; production has
// not looked like that since llm_serve.go began arming wrapStreams, and the
// copier parked in Read then outlived RunInteractive forever.
//
// The same fixture guard applies: stdin must be non-nil and the child must be
// shown to have RECEIVED a byte through it, or the copier goroutine was never
// started and VerifyNone would be green over nothing.
func TestRunInteractive_WrappedStdinGoroutineDoesNotLeak(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = stdinW.Write([]byte("ping\n")) }()

	ignore := goleak.IgnoreCurrent()

	cmd := exec.Command("sh", "-c", "read line; printf 'got %s\\n' \"$line\"; sleep 0.1")
	var out bytes.Buffer
	exitCode, err := RunInteractive(context.Background(), cmd, wrappedStdin{stdinR}, func() { _ = stdinR.Close() }, &out, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	// Fixture guard: the copier demonstrably ran and delivered a byte. Without
	// this the leak check below could pass because nothing was ever started.
	require.Contains(t, out.String(), "got ping",
		"the stdin copier must have started and delivered stdin into the pty")

	// The copier is parked in a Read on the WRAPPED reader. Only the caller's
	// explicit cleanup can unpark it — the test deliberately does nothing to
	// help, which is the property being pinned.
	goleak.VerifyNone(t, ignore)

	_ = stdinW.Close()
}

// TestRunInteractive_BenignPTYCloseSwallowed confirms the errors.Is-based
// benign-error handling (also subarctic-backed-garnet): closing the PTY after
// the command exits produces fs.ErrClosed / syscall.EIO fallout that must be
// swallowed, so a normal interactive run returns cleanly rather than surfacing
// a spurious "command failed".
func TestRunInteractive_BenignPTYCloseSwallowed(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'line one\\nline two\\n'; sleep 0.1")
	var out bytes.Buffer
	exitCode, err := RunInteractive(context.Background(), cmd, nil, nil, &out, nil)
	require.NoError(t, err, "benign PTY-close fallout must be swallowed via errors.Is")
	assert.Equal(t, 0, exitCode)
	assert.Contains(t, out.String(), "line one")
	assert.Contains(t, out.String(), "line two")
}
