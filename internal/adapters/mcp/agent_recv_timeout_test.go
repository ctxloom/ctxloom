package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// A timed-out agent_recv is two different events depending on who parked.
// For a COORDINATOR a quiet window means "nothing arrived; receive again" —
// rendered as a failed tool call it is the same misleading red that drove the
// retry loop the yield fix cured, so it completes successfully with no
// messages and a disposition saying so. For a LEAF the timeout is the signal
// to stop, and its harness should show red, so the error stands. The two
// audiences get DIFFERENT verdict shapes for the same event on purpose; the
// leaf tests here exist so nobody "unifies" that back.

// The coordinator's verdict, on the session endpoint's handler
// (runnermcp.RecvHandler, the one surface an engine dials).
func TestRecvHandler_CoordinatorTimeoutIsASuccessfulEmptyReceive(t *testing.T) {
	h := runnermcp.RecvHandler(report.To(nil), testHome(t), false)

	res, err := runnerRecv(t, h, 1)
	require.NoError(t, err, "a coordinator's quiet wait is not a failure")
	require.NotNil(t, res)
	shape, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content is the object the schema declares; got %T", res.StructuredContent)
	assert.Empty(t, shape["messages"])
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, shape["disposition"])
}

func TestRecvHandler_LeafTimeoutStaysAnError(t *testing.T) {
	h := runnermcp.RecvHandler(report.To(nil), testHome(t), true)

	res, err := runnerRecv(t, h, 1)
	require.ErrorIs(t, err, coord.ErrRecvTimeout, "a leaf's timeout is its signal to stop, and its harness should show red")
	assert.Nil(t, res, "a leaf timeout carries no successful result to mistake for an empty receive")
	assert.Contains(t, err.Error(), runnermcp.RecvTimeoutLeafGuidance)
}
