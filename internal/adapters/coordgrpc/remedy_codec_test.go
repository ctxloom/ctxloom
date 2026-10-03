package coordgrpc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
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
			resp := overTheWire(t, &agentcoordpb.RunnerResponse{RequestId: "r-1", Status: RefusalStatus(code, cause)}, &agentcoordpb.RunnerResponse{})
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
	err := ErrFromStatus(resp.GetStatus())
	require.Error(t, err)
	assert.Equal(t, cause.Error(), err.Error())
	fix, ok := clifmt.RemedyOf(err)
	assert.True(t, ok)
	assert.Equal(t, wireRemedy, fix)
}

// TestRefusalStatus_NoRemedyNoDetail: an error that names no fix — plain, or
// a report.Error whose Fix is empty — attaches no detail at all.
func TestRefusalStatus_NoRemedyNoDetail(t *testing.T) {
	for _, err := range []error{errors.New("plain"), report.Error{Msg: "listing"}} {
		st := RefusalStatus(codes.Internal, err)
		assert.Empty(t, st.GetDetails(), "%v", err)
		assert.Equal(t, err.Error(), st.GetMessage())
		_, ok := clifmt.RemedyOf(ErrFromStatus(st))
		assert.False(t, ok, "nothing to decode as a remedy")
	}
}

// TestRefusalStatus_RemedyHiddenBehindAnEmptyWrapper: a remediable wrapper
// with nothing to add must not hide the fix its cause names.
func TestRefusalStatus_RemedyHiddenBehindAnEmptyWrapper(t *testing.T) {
	err := report.Error{Msg: "outer", Err: report.Errorf(wireRemedy, "inner")}
	fix, ok := clifmt.RemedyOf(ErrFromStatus(RefusalStatus(codes.Internal, err)))
	assert.True(t, ok)
	assert.Equal(t, wireRemedy, fix)
}

// TestErrFromStatus_UnknownDetailIsIgnored: a status carrying a detail this
// build does not read decodes to the refusal, not to a decode failure.
func TestErrFromStatus_UnknownDetailIsIgnored(t *testing.T) {
	other, err := anypb.New(&errdetails.ErrorInfo{Reason: "SOMETHING_ELSE", Domain: "example.test"})
	require.NoError(t, err)
	st := overTheWire(t, &rpcstatus.Status{Code: int32(codes.FailedPrecondition), Message: "refused", Details: []*anypb.Any{other}}, &rpcstatus.Status{})
	got := ErrFromStatus(st)
	require.Error(t, got)
	assert.Equal(t, "refused", got.Error())
	_, ok := clifmt.RemedyOf(got)
	assert.False(t, ok)
}

// TestErrFromStatus_OKIsNil: an accepted answer is no error.
func TestErrFromStatus_OKIsNil(t *testing.T) {
	assert.NoError(t, ErrFromStatus(OKStatus("fine")))
	assert.NoError(t, ErrFromStatus(nil))
}
