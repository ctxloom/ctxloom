package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// A timed-out agent_recv is two different events depending on who parked.
// For a COORDINATOR a quiet window means "nothing arrived; receive again" —
// rendered as a failed tool call it is the same misleading red that drove the
// retry loop the yield fix cured, so it completes successfully with no
// messages and a disposition saying so. For a LEAF the timeout is the signal
// to stop, and its harness should show red, so the error stands. The two
// audiences get DIFFERENT verdict shapes for the same event on purpose; the
// leaf tests here exist so nobody "unifies" that back.

func stdioServerAs(t *testing.T, self coord.Identity) *ctxServer {
	t.Helper()
	cfg, c, _ := buildHostCoordinator(t, nil)
	return &ctxServer{cfg: cfg, self: self, agents: &agentDelegation{self: self, c: c}}
}

func TestHandleAgentRecv_CoordinatorTimeoutIsASuccessfulEmptyReceive(t *testing.T) {
	s := stdioServerAs(t, coord.Identity{Harp: "coordinator-harp", Depth: 0})

	_, out, err := s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
	require.NoError(t, err, "a coordinator's quiet wait is not a failure: rendered as one, the harness shows red and the caller retries into it")
	require.NotNil(t, out)
	assert.Empty(t, out.Messages, "a timed-out receive delivers nothing")
	shape := wireShape(t, out)
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, shape["disposition"],
		"the result must SAY it timed out and that receiving again is the move")
	assert.NotContains(t, mcpschema.RecvDispositionTimedOut, instructionToFinish,
		"a coordinator on a quiet wait re-arms; telling it to finish is the child's instruction")
}

// A leaf's agent_recv is served by ITS RUNNER (recvHandler, below), which
// drains the run's own spool. The coordinator-side stdio server receives for
// the session owner only; a leaf identity reaching it is refused rather
// than parked on an inbox it does not have.
func TestHandleAgentRecv_LeafIsRefusedAtTheCoordinator(t *testing.T) {
	s := stdioServerAs(t, coord.Identity{Harp: "child-harp", Depth: 1})

	_, out, err := s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
	require.ErrorIs(t, err, coord.ErrRecvNotOwner, "a leaf has no inbox at the coordinator; its runner drains its spool")
	assert.Nil(t, out, "a refusal carries no successful result to mistake for an empty receive")
}

func TestRecvHandler_CoordinatorTimeoutIsASuccessfulEmptyReceive(t *testing.T) {
	h := recvHandler(testHome(t), false)

	res, err := runnerRecv(t, h, 1)
	require.NoError(t, err, "a coordinator's quiet wait is not a failure")
	require.NotNil(t, res)
	shape, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content is the object the schema declares; got %T", res.StructuredContent)
	assert.Empty(t, shape["messages"])
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, shape["disposition"])
}

func TestRecvHandler_LeafTimeoutStaysAnError(t *testing.T) {
	h := recvHandler(testHome(t), true)

	res, err := runnerRecv(t, h, 1)
	require.ErrorIs(t, err, coord.ErrRecvTimeout, "a leaf's timeout is its signal to stop, and its harness should show red")
	assert.Nil(t, res, "a leaf timeout carries no successful result to mistake for an empty receive")
	assert.Contains(t, err.Error(), recvTimeoutLeafGuidance)
}
