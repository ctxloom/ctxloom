package grpc

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nilResultBackend returns the shape a buggy backend can produce: no result and
// no error.
type nilResultBackend struct{ fakeBackend }

func (b *nilResultBackend) Execute(context.Context, *agent.ExecuteRequest, io.Writer, io.Writer) (*agent.ExecuteResult, error) {
	return nil, nil
}

// TestRunTurn_RefusesASetupFailure is the inversion of what this file used to
// assert. The subtest here was "a Setup failure still launches the engine", and
// it required RunTurn to return no error and run Execute anyway.
//
// feeble-sway ruled that behaviour out. Setup is what delivers the run's
// context, its MCP servers and its hooks; a turn that launches without them is
// not a degraded version of the run the user asked for, it is a different run
// reporting success at exit 0 — the silent no-op this project refuses. So the
// error now reaches the caller and Execute never runs.
//
// The two genuine degradations kept their old home below: this one moved out
// because it is no longer a degradation at all, and leaving it in a test named
// "DegradesInsteadOfAborting" would have left that name asserting the contract
// the ruling removed.
func TestRunTurn_RefusesASetupFailure(t *testing.T) {
	fb := &fakeBackend{name: "mock", setupErr: assert.AnError, captureStdout: "engine ran"}
	var out strings.Builder

	result, err := RunTurn(context.Background(), fb, &RunStart{
		Prompt:  &Fragment{Content: "go"},
		Options: &RunOptions{WorkDir: "/work"},
	}, nil, nil, &out, &out, nil, nil)

	require.Error(t, err, "a Setup failure must abort the turn rather than launch an engine that was never given its context, MCP servers or hooks")
	assert.ErrorIs(t, err, assert.AnError, "the refusal must wrap the underlying Setup error so a caller can still branch on the cause")
	assert.Nil(t, result, "a refused turn yields no result")
	assert.True(t, fb.setupCalled, "Setup must actually have been attempted")
	assert.NotContains(t, out.String(), "engine ran",
		"Execute must NOT run after a failed Setup: launching here is exactly the silent no-op — an engine with none of its surfaces delivered, reporting success")
}

// RunTurn degrades on two faults rather than aborting the turn: a teardown
// hiccup must not mask a completed run, and a backend that returns (nil, nil)
// must not nil-dereference the serving goroutine. Neither was pinned before
// this test.
//
// A Setup failure is NOT among them — see TestRunTurn_RefusesASetupFailure.
func TestRunTurn_DegradesInsteadOfAborting(t *testing.T) {
	t.Run("a Cleanup failure does not mask a successful run", func(t *testing.T) {
		fb := &fakeBackend{
			name:          "mock",
			cleanupErr:    assert.AnError,
			executeResult: &agent.ExecuteResult{ExitCode: 3},
		}

		result, err := RunTurn(context.Background(), fb, &RunStart{Options: &RunOptions{}},
			nil, nil, &strings.Builder{}, &strings.Builder{}, nil, nil)

		require.NoError(t, err, "partial success is success: teardown must not become the verdict")
		require.NotNil(t, result)
		assert.Equal(t, int32(3), result.ExitCode)
		assert.True(t, fb.cleanupCalled)
	})

	t.Run("a nil result with no error degrades to exit 0", func(t *testing.T) {
		b := &nilResultBackend{fakeBackend: fakeBackend{name: "mock"}}

		result, err := RunTurn(context.Background(), b, &RunStart{Options: &RunOptions{}},
			nil, nil, &strings.Builder{}, &strings.Builder{}, nil, nil)

		require.NoError(t, err)
		require.NotNil(t, result, "a nil result must not reach the caller")
		assert.Equal(t, int32(0), result.ExitCode)
	})
}
