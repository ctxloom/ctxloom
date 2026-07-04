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
