package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestQueryPendingApprovals_ReadsTheQueue: a discovered coordinator answers
// with its parked requests, every field intact, over the credential its
// endpoint.json carries.
func TestQueryPendingApprovals_ReadsTheQueue(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvals = &agentcoordpb.PendingApprovalsResult{
		ProjectDir: "/proj",
		Pending: []*agentcoordpb.PendingApprovalsResult_Pending{{
			Kind: agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL, Harp: "child-a", Agent: "worker",
			Lineage: []string{"root", "child-a"}, Summary: "Bash: make",
		}},
	}
	startFakeCoordinator(t, home, "proj", f)
	eps, skipped := discover.List()
	require.Empty(t, skipped)
	require.Len(t, eps, 1)

	got, err := QueryPendingApprovals(context.Background(), eps[0])
	require.NoError(t, err)
	assert.Equal(t, "/proj", got.GetProjectDir())
	require.Len(t, got.GetPending(), 1)
	assert.Equal(t, "Bash: make", got.GetPending()[0].GetSummary())
	assert.Equal(t, []string{"root", "child-a"}, got.GetPending()[0].GetLineage())
}

// TestQueryPendingApprovals_DeadEndpointIsUnavailable: an endpoint.json that
// outlived its coordinator errs, naming the endpoint, with the gRPC status
// intact so a caller can tell "nobody is there" from a refusal.
func TestQueryPendingApprovals_DeadEndpointIsUnavailable(t *testing.T) {
	ep := discover.Endpoint{URL: discover.LoopbackURL(1), Cred: "stale"} // port 1: nothing listens
	_, err := QueryPendingApprovals(context.Background(), ep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ep.URL)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
