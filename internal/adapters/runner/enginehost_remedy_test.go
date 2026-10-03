package runner

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/remedystatus"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

const startRunRemedy = "run `ctxloom doctor` and fix the container runtime it names"

// failingRunner is a Runner whose launch fails with err.
type failingRunner struct{ err error }

func (r failingRunner) Execute(context.Context, *agentcoordpb.Launch) error { return r.err }

// TestEngineHost_StartRunRefusalCarriesTheRemedy: a launch that fails naming
// its own fix answers StartRun with that fix on the status, on both refusal
// arms — the plain one and the endpoint-unavailable (rebind) one.
func TestEngineHost_StartRunRefusalCarriesTheRemedy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		code  codes.Code
	}{
		{"launch fault", report.Errorf(startRunRemedy, "container runtime missing"), codes.InvalidArgument},
		{"endpoint unavailable", report.Errorf(startRunRemedy, "bind: %w", delivery.ErrEndpointUnavailable), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eh := NewEngineHost(context.Background(), nil, "claude-code", "run-1")
			t.Cleanup(eh.Close)
			eh.BindHome(&fakeEngineHome{})
			eh.BindRunner(failingRunner{err: fmt.Errorf("execute: %w", tc.cause)})
			resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{
				StartRun: &agentcoordpb.StartRun{RunId: "run-1"},
			}})
			require.EqualValues(t, tc.code, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
			fix, ok := clifmt.RemedyOf(remedystatus.Err(resp.GetStatus()))
			assert.True(t, ok, "the refusal carries its remedy")
			assert.Equal(t, startRunRemedy, fix)
		})
	}
}
