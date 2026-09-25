package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
)

// errRunnerDied is the exit a dyingPolicy's runner reports.
var errRunnerDied = errors.New("test: runner exited before dialing home")

// dyingPolicy is a stubPolicy whose runner reports errRunnerDied from Wait.
type dyingPolicy struct{ stubPolicy }

func (dyingPolicy) StartRunner(context.Context, string, string, int, isolation.Workspace, map[string]string) (*isolation.RunnerHandle, error) {
	return &isolation.RunnerHandle{Kill: func() {}, Wait: func() error { return errRunnerDied }}, nil
}

// RunnerStarter must hand the runner's own exit waiter to the coordinator:
// it is the only signal that ends the dial-home wait on a runner that died,
// and a starter that dropped it would leave `ctxloom run` silent for the
// whole dial-home budget.
func TestRunnerStarter_HandsOverTheRunnerWait(t *testing.T) {
	start := RunnerStarter(PreparedCell{Policy: dyingPolicy{}, Workspace: stubWorkspace{dir: t.TempDir()}}, "mock", "fast", 0, nil)
	runner, err := start(context.Background(), map[string]string{})
	require.NoError(t, err)
	require.NotNil(t, runner.Wait, "the starter must carry the runner's exit waiter")
	require.ErrorIs(t, runner.Wait(), errRunnerDied)
	require.NotNil(t, runner.Kill)
}
