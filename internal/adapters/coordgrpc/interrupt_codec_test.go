package coordgrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// An InterruptRun's answer reaches the coordinator with its verdict, so a
// steer can say whether it cut a running turn; both values decode.
func TestRunnerResponseFromWire_CarriesTheInterruptVerdict(t *testing.T) {
	for _, cut := range []bool{true, false} {
		got := RunnerResponseFromWire(&agentcoordpb.RunnerResponse{
			Status: OKStatus(""),
			Kind:   &agentcoordpb.RunnerResponse_InterruptRun{InterruptRun: &agentcoordpb.InterruptRunResult{Interrupted: cut}},
		})
		assert.NoError(t, got.Err)
		assert.Equal(t, coord.InterruptRunResult{Interrupted: cut}, got.Kind)
	}
}
