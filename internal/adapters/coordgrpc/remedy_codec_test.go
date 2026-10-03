package coordgrpc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/remedystatus"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

const wireRemedy = "declare `auth: token` on a container agent, or run it with `runtime: host`"

// overTheWire marshals m and unmarshals it into a fresh value: what the far
// side of the connection actually holds, not the encoder's in-memory value.
func overTheWire[M proto.Message](t *testing.T, m M, fresh M) M {
	t.Helper()
	raw, err := proto.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, fresh))
	return fresh
}

// TestRunnerResponse_StartRunRefusalCarriesItsRemedy: a runner refusing
// StartRun with an error that names its fix delivers that fix to the
// coordinator — the decoded Err is remediable with the same fix and the same
// text — on both the plain arm and the UNAVAILABLE (rebind) arm.
func TestRunnerResponse_StartRunRefusalCarriesItsRemedy(t *testing.T) {
	cause := fmt.Errorf("deliver launch: %w", report.Errorf(wireRemedy, "container: no credential to forward"))
	for _, code := range []codes.Code{codes.InvalidArgument, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			resp := overTheWire(t, &agentcoordpb.RunnerResponse{RequestId: "r-1", Status: remedystatus.Refusal(code, cause)}, &agentcoordpb.RunnerResponse{})
			got := RunnerResponseFromWire(resp)
			require.Error(t, got.Err)
			fix, ok := clifmt.RemedyOf(got.Err)
			assert.True(t, ok, "the refusal decodes remediable")
			assert.Equal(t, wireRemedy, fix)
			assert.Contains(t, got.Err.Error(), cause.Error(), "the refusal's own text survives")
			assert.Equal(t, code == codes.Unavailable, errors.Is(got.Err, coord.ErrRunnerUnavailable), "only UNAVAILABLE is the rebind sentinel")
		})
	}
}

// TestAgentReply_RefusalCarriesItsRemedy: an agent_run refusal that names its
// fix crosses StatusFromErr (via AgentReplyToWire) and decodes on the agent's
// side with that fix, its message and its code unchanged.
func TestAgentReply_RefusalCarriesItsRemedy(t *testing.T) {
	cause := fmt.Errorf("agent_run: %w", report.Error{Msg: "agent \"worker\" cannot launch", Fix: wireRemedy, Err: coord.ErrInvalidRequest})
	resp := overTheWire(t, AgentReplyToWire(coord.AgentReply{RequestID: "r-2", Err: cause}), &agentcoordpb.CoordinatorResponse{})
	assert.EqualValues(t, codes.InvalidArgument, resp.GetStatus().GetCode(), "the remedy does not disturb the code table")
	err := remedystatus.Err(resp.GetStatus())
	require.Error(t, err)
	assert.Equal(t, cause.Error(), err.Error())
	fix, ok := clifmt.RemedyOf(err)
	assert.True(t, ok)
	assert.Equal(t, wireRemedy, fix)
}
