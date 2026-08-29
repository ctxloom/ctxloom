package grpc

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nilResultBackend returns the shape a buggy backend can produce: no result and
// no error.
type nilResultBackend struct{ fakeBackend }

func (b *nilResultBackend) Execute(context.Context, *agent.ExecuteRequest, io.Writer, io.Writer) (*agent.ExecuteResult, error) {
	return nil, nil
}

// RunTurn degrades on three faults rather than aborting the turn. All three are
// stated invariants (fault tolerance, CLAUDE.md) and none of them was pinned:
// Setup is load-bearing but non-essential, so a failure warns and the engine
// still launches; a teardown hiccup must not mask a completed run; and a
// backend that returns (nil, nil) must not nil-dereference the serving
// goroutine.
func TestRunTurn_DegradesInsteadOfAborting(t *testing.T) {
	t.Run("a Setup failure still launches the engine", func(t *testing.T) {
		fb := &fakeBackend{name: "mock", setupErr: assert.AnError, captureStdout: "engine ran"}
		var out strings.Builder

		result, err := RunTurn(context.Background(), fb, &RunStart{
			Prompt:  &Fragment{Content: "go"},
			Options: &RunOptions{WorkDir: "/work"},
		}, nil, &out, &out, nil, nil)

		require.NoError(t, err, "a Setup failure must not abort the turn")
		require.NotNil(t, result)
		assert.True(t, fb.setupCalled)
		assert.Contains(t, out.String(), "engine ran", "Execute ran anyway")
	})

	t.Run("a Cleanup failure does not mask a successful run", func(t *testing.T) {
		fb := &fakeBackend{
			name:          "mock",
			cleanupErr:    assert.AnError,
			executeResult: &agent.ExecuteResult{ExitCode: 3},
		}

		result, err := RunTurn(context.Background(), fb, &RunStart{Options: &RunOptions{}},
			nil, &strings.Builder{}, &strings.Builder{}, nil, nil)

		require.NoError(t, err, "partial success is success: teardown must not become the verdict")
		require.NotNil(t, result)
		assert.Equal(t, int32(3), result.ExitCode)
		assert.True(t, fb.cleanupCalled)
	})

	t.Run("a nil result with no error degrades to exit 0", func(t *testing.T) {
		b := &nilResultBackend{fakeBackend: fakeBackend{name: "mock"}}

		result, err := RunTurn(context.Background(), b, &RunStart{Options: &RunOptions{}},
			nil, &strings.Builder{}, &strings.Builder{}, nil, nil)

		require.NoError(t, err)
		require.NotNil(t, result, "a nil result must not reach the caller")
		assert.Equal(t, int32(0), result.ExitCode)
	})
}
