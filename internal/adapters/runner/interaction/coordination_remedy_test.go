package interaction

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

const agentRunRemedy = "add the agent with `ctxloom agent set worker`"

// refusedOverTheWire is the agent-side copy of the coordinator's reply
// refusing with err.
func refusedOverTheWire(t *testing.T, err error) *agentcoordpb.CoordinatorResponse {
	t.Helper()
	raw, merr := proto.Marshal(coordgrpc.AgentReplyToWire(coord.AgentReply{RequestID: "r-1", Err: err}))
	require.NoError(t, merr)
	var resp agentcoordpb.CoordinatorResponse
	require.NoError(t, proto.Unmarshal(raw, &resp))
	return &resp
}

// TestCoordinationResult_RefusalShowsItsFixLine: the MCP SDK reports a
// handler's error to the model as err.Error() alone, so an agent_run refusal
// naming a fix must carry that fix IN the text, in clifmt.FixLine's form —
// and the error stays remediable for anything reading it structurally.
func TestCoordinationResult_RefusalShowsItsFixLine(t *testing.T) {
	cause := fmt.Errorf("agent_run: %w", report.Errorf(agentRunRemedy, "no agent named %q", "worker"))
	_, err := coordinationResult(refusedOverTheWire(t, cause), nil)
	require.Error(t, err)
	assert.Equal(t, cause.Error()+clifmt.FixLine("", agentRunRemedy), err.Error())
	fix, ok := clifmt.RemedyOf(err)
	assert.True(t, ok)
	assert.Equal(t, agentRunRemedy, fix)
}

// TestCoordinationResult_RefusalWithoutAFixIsItsMessage: no remedy, no fix
// line — the text is the refusal's message, unchanged.
func TestCoordinationResult_RefusalWithoutAFixIsItsMessage(t *testing.T) {
	cause := errors.New("agent_run: refused")
	_, err := coordinationResult(refusedOverTheWire(t, cause), nil)
	require.Error(t, err)
	assert.Equal(t, cause.Error(), err.Error())
}
