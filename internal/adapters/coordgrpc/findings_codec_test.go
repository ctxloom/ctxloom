package coordgrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// What the runner reported while executing a launch crosses the StartRun
// answer as the advisory the originator renders — text, remedy, once — the
// same line the runner's own stderr would have shown, on a run whose stderr
// nobody sees. A fail-loudly kind does not cross: the runner already acted
// on it. Round-tripped through the wire bytes, as it travels.
//
// MUTATION: drop any field from EncodeFindings or the decode → red.
func TestStartRunResult_FindingsRoundTripTheWire(t *testing.T) {
	sent := report.Findings{
		{Text: "command /deploy skipped: its name collides with a built-in", Remedy: "rename the command", Once: true},
		{Kind: report.KindApply, Text: "runner: sweep: reverse departed sessions", Remedy: "a later run's sweep retries"},
	}
	wire := &agentcoordpb.RunnerResponse{
		Status: OKStatus(""),
		Kind:   &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{Pid: 7, Findings: EncodeFindings(sent)}},
	}
	b, err := proto.Marshal(wire)
	require.NoError(t, err)
	var back agentcoordpb.RunnerResponse
	require.NoError(t, proto.Unmarshal(b, &back))

	got := RunnerResponseFromWire(&back)
	require.NoError(t, got.Err)
	res, ok := got.Kind.(coord.StartRunResult)
	require.True(t, ok)
	assert.Equal(t, int64(7), res.PID)
	assert.Equal(t, report.Findings{
		{Text: sent[0].Text, Remedy: sent[0].Remedy, Once: true},
		{Text: sent[1].Text, Remedy: sent[1].Remedy},
	}, res.Findings)
}

// No findings is no findings: nothing invented on the way.
func TestStartRunResult_NoFindingsDecodesToNone(t *testing.T) {
	got := RunnerResponseFromWire(&agentcoordpb.RunnerResponse{
		Status: OKStatus(""),
		Kind:   &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{}},
	})
	res, ok := got.Kind.(coord.StartRunResult)
	require.True(t, ok)
	assert.Empty(t, res.Findings)
	assert.Empty(t, EncodeFindings(nil))
}
