//go:build !windows

package cmd

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exitErrorFrom runs cmdline under sh and returns the resulting
// *exec.ExitError, failing the test if it exits cleanly.
func exitErrorFrom(t *testing.T, cmdline string) *exec.ExitError {
	t.Helper()
	err := exec.Command("sh", "-c", cmdline).Run()
	require.Error(t, err)
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "expected *exec.ExitError, got %T", err)
	return exitErr
}

func TestChildExitCode_PlainNonZeroExitPassesThrough(t *testing.T) {
	assert.Equal(t, 7, childExitCode(exitErrorFrom(t, "exit 7")))
}

// A signal death must map to the conventional 128+signal, not the raw
// ExitCode() of -1 (which os.Exit would render as 255).
func TestChildExitCode_SignalDeathMapsTo128PlusSignal(t *testing.T) {
	exitErr := exitErrorFrom(t, "kill -TERM $$")
	assert.Equal(t, 128+15, childExitCode(exitErr), "SIGTERM death should report 143")
}
