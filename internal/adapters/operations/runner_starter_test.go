package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// errRunnerDied is the exit a dying environment's runner reports.
var errRunnerDied = errors.New("test: runner exited before dialing home")

// RunnerStarter must hand the runner's own exit waiter to the coordinator:
// it is the only signal that ends the dial-home wait on a runner that died,
// and a starter that dropped it would leave `ctxloom run` silent for the
// whole dial-home budget.
func TestRunnerStarter_HandsOverTheRunnerWait(t *testing.T) {
	dying := stubEnvAt(t.TempDir(), nil)
	dying.wait = func() error { return errRunnerDied }
	start := RunnerStarter(dying, "mock", "fast", 0, nil)
	runner, err := start(context.Background(), map[string]string{})
	require.NoError(t, err)
	require.NotNil(t, runner.Wait, "the starter must carry the runner's exit waiter")
	require.ErrorIs(t, runner.Wait(), errRunnerDied)
	require.NotNil(t, runner.Kill)
}
