package backends

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestRunLaunchSpec_NonInteractiveWiresStdin verifies a non-interactive launch
// feeds the provided stdin reader to the child. That is the channel a backend
// uses to deliver a large oneshot prompt off the argv (which the OS
// length-limits), so it must actually reach the process. Uses `cat`, which
// echoes stdin to stdout and exits on EOF.
func TestRunLaunchSpec_NonInteractiveWiresStdin(t *testing.T) {
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}
	const body = "piped-task-body-that-never-touches-argv"
	var out bytes.Buffer
	code, err := RunLaunchSpec(
		context.Background(),
		agent.LaunchSpec{BinaryPath: catPath, Interactive: false},
		strings.NewReader(body),
		&out, io.Discard, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, int32(0), code)
	assert.Equal(t, body, out.String(), "child must receive the stdin the launcher was given")
}

// TestRunLaunchSpec_NonInteractiveNilStdin verifies a nil stdin still runs (the
// child reads the null device and gets immediate EOF) rather than blocking — the
// behavior a run that takes no input relies on.
func TestRunLaunchSpec_NonInteractiveNilStdin(t *testing.T) {
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}
	var out bytes.Buffer
	code, err := RunLaunchSpec(
		context.Background(),
		agent.LaunchSpec{BinaryPath: catPath, Interactive: false},
		nil,
		&out, io.Discard, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, int32(0), code)
	assert.Empty(t, out.String())
}

// TestRunLaunchSpec_NonInteractiveSignalKilledChildYields128PlusSignum pins the
// non-interactive half of the same exit-status contract the pty branch keeps: a
// child that died on a signal reports 128+signum, not the raw -1 os/exec hands
// back (which is not a valid exit status and reaches the user truncated to
// 255). The two branches classify a killed engine identically, so the status a
// user sees does not depend on whether the run happened to be interactive.
// Skips where there is no POSIX shell to signal itself with — the mapping there
// is documented as a no-op (see ptyrunner.ExitStatusFor).
func TestRunLaunchSpec_NonInteractiveSignalKilledChildYields128PlusSignum(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	for _, tt := range []struct {
		name         string
		signal       string
		expectedCode int32
	}{
		{name: "SIGINT", signal: "INT", expectedCode: 130},
		{name: "SIGKILL", signal: "KILL", expectedCode: 137},
		{name: "SIGTERM", signal: "TERM", expectedCode: 143},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, err := RunLaunchSpec(
				context.Background(),
				agent.LaunchSpec{
					BinaryPath:  shPath,
					Args:        []string{"-c", "kill -" + tt.signal + " $$; sleep 5"},
					Interactive: false,
				},
				nil,
				io.Discard, io.Discard, nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedCode, code,
				"a child killed by SIG%s must report 128+signum", tt.signal)
		})
	}
}
