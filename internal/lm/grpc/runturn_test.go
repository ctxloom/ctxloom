package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestRunTurn_DrivesSetupExecuteCleanup pins the shared body `ctxloom llm turn`
// reuses (Phase 2a): RunTurn runs Setup → Execute → Cleanup against plain
// stdio, hands the prompt to Execute, and returns the engine result — no stream
// involved. This is the exact body the go-plugin Run RPC also runs, so a break
// here breaks both interactive transports.
func TestRunTurn_DrivesSetupExecuteCleanup(t *testing.T) {
	fb := &fakeBackend{
		name:          "mock",
		captureStdout: "hello-from-engine",
		executeResult: &agent.ExecuteResult{ExitCode: 5},
	}
	var out strings.Builder
	req := &RunStart{
		Prompt:  &Fragment{Content: "do the thing"},
		Options: &RunOptions{WorkDir: "/work"},
	}

	result, err := RunTurn(context.Background(), fb, req, nil, nil, &out, &out, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, fb.setupCalled, "Setup ran")
	assert.True(t, fb.cleanupCalled, "Cleanup ran")
	assert.Equal(t, "do the thing", fb.capturedPrompt, "the prompt reached Execute")
	assert.Contains(t, out.String(), "hello-from-engine", "engine output reached the plain stdout")
	assert.Equal(t, int32(5), result.ExitCode, "the engine exit code is returned to the caller")
}

// TestRunTurn_ExecuteErrorPropagates: an Execute error returns from RunTurn
// (still after Cleanup), matching the go-plugin path's semantics.
func TestRunTurn_ExecuteErrorPropagates(t *testing.T) {
	fb := &fakeBackend{name: "mock", executeErr: assert.AnError}
	_, err := RunTurn(context.Background(), fb, &RunStart{Options: &RunOptions{}}, nil, nil, &strings.Builder{}, &strings.Builder{}, nil, nil)
	require.Error(t, err)
	assert.True(t, fb.cleanupCalled, "Cleanup still runs on an Execute failure")
}
