package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// agent_recv allows one live receive per session: a newer receive supersedes
// an older parked one. That supersession is a YIELD, not a failure — nothing
// is lost, the newer receive holds the park — yet it used to surface as a
// plain error, which a harness renders as a failed tool call. A coordinator
// reading "failed" retries, and the retry preempts the receive that was about
// to deliver; observed more than six times in one session, every one caused
// by the verdict itself. These tests pin the contract that stops that loop on
// the session endpoint's handler (interaction.RecvHandler): the superseded
// call completes successfully with no messages and a disposition saying so.
// The timeout verdicts — a coordinator's is a success, a leaf's an error —
// are pinned beside these in agent_recv_timeout_test.go.

// instructionToFinish is the word the child's guidance turns on. The pins
// below check for it directly, not only for the guidance constant: the defect
// was a coordinator obeying "finish", and a pin on the constant alone would
// let a rephrased copy of the same instruction back in.
const instructionToFinish = "finish"

// TestRecvOutcome_TimeoutVerdictFollowsTheAudience: one sentinel, two
// audiences, two verdict SHAPES. The coord sentinel cannot know who is
// parked, so the classification happens where the audience is known: a
// leaf's timeout is an error carrying the child's instruction, with the
// sentinel identity intact for every errors.Is caller; a coordinator's is a
// successful empty receive that never sees that instruction.
func TestRecvOutcome_TimeoutVerdictFollowsTheAudience(t *testing.T) {
	disposition, leaf := interaction.RecvOutcome(coord.ErrRecvTimeout, 5*time.Second, true)
	require.ErrorIs(t, leaf, coord.ErrRecvTimeout)
	assert.Empty(t, disposition, "a leaf's timeout is a failure, not a disposition")
	assert.Contains(t, leaf.Error(), interaction.RecvTimeoutLeafGuidance)

	disposition, coordinator := interaction.RecvOutcome(coord.ErrRecvTimeout, 5*time.Second, false)
	require.NoError(t, coordinator, "a coordinator's timeout is not a failure")
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, disposition)
	assert.NotContains(t, disposition, instructionToFinish)

	assert.NotContains(t, coord.ErrRecvTimeout.Error(), instructionToFinish,
		"the shared sentinel must stay audience-neutral; the child's instruction belongs only where a child reads it")

	for _, leaf := range []bool{true, false} {
		disposition, failure := interaction.RecvOutcome(coord.ErrRecvPreempted, time.Second, leaf)
		require.NoError(t, failure, "a yield is a success for every audience")
		assert.Equal(t, mcpschema.RecvDispositionYielded, disposition)
	}

	other := errors.New("something else")
	disposition, failure := interaction.RecvOutcome(other, time.Second, true)
	assert.Equal(t, other, failure, "only the timeout gains guidance")
	assert.Empty(t, disposition)
}

// The runner surface parks locally in the Home, so the supersession is
// observable with no coordinator behind it.
func runnerRecv(t *testing.T, h mcp.ToolHandler, wait int) (*mcp.CallToolResult, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"wait": wait})
	require.NoError(t, err)
	return h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}})
}

type runnerRecvOutcome struct {
	res *mcp.CallToolResult
	err error
}

func TestRecvHandler_SupersededReceiveYieldsAsSuccess(t *testing.T) {
	h := interaction.RecvHandler(report.To(nil), testHome(t), false)

	outcomes := make(chan runnerRecvOutcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, err := runnerRecv(t, h, 2)
			outcomes <- runnerRecvOutcome{res: res, err: err}
		}()
	}

	var yield runnerRecvOutcome
	select {
	case yield = <-outcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("neither receive completed")
	}
	require.NoError(t, yield.err, "a superseded receive is a yield, never an error")
	shape, ok := yield.res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content is the object the schema declares; got %T", yield.res.StructuredContent)
	assert.Empty(t, shape["messages"])
	assert.Equal(t, mcpschema.RecvDispositionYielded, shape["disposition"])

	// The survivor then times out on the quiet Home — as a coordinator, so
	// successfully, with no instruction to finish.
	var survivor runnerRecvOutcome
	select {
	case survivor = <-outcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("the surviving receive never completed")
	}
	require.NoError(t, survivor.err)
	shape, ok = survivor.res.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, shape["disposition"])
}
