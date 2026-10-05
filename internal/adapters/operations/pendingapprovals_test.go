package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestQueryPendingApprovals_ReadsTheQueue: a live coordinator answers with
// its parked requests, every field intact, over the credential its
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

	answers, failures := QueryPendingApprovals(context.Background())
	require.Empty(t, failures)
	require.Len(t, answers, 1)
	got := answers[0]
	assert.Equal(t, "/proj", got.GetProjectDir())
	require.Len(t, got.GetPending(), 1)
	assert.Equal(t, "Bash: make", got.GetPending()[0].GetSummary())
	assert.Equal(t, []string{"root", "child-a"}, got.GetPending()[0].GetLineage())
}

// TestQueryPendingApprovals_NothingRunning: no coordinator, and a root
// whose endpoint nobody answers on, are both "nothing is running" — no
// answer and no failure.
func TestQueryPendingApprovals_NothingRunning(t *testing.T) {
	home := testsupport.Isolate(t)
	answers, failures := QueryPendingApprovals(context.Background())
	assert.Empty(t, answers)
	assert.Empty(t, failures)

	writeUnansweringEndpoint(t, home, "gone")
	answers, failures = QueryPendingApprovals(context.Background())
	assert.Empty(t, answers)
	assert.Empty(t, failures, "an endpoint nobody answers on is not a failure")
}

// TestQueryPendingApprovals_RefusalIsAFailure: a coordinator that answered
// with an error is a failure, its gRPC status intact.
func TestQueryPendingApprovals_RefusalIsAFailure(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvalsErr = status.Error(codes.PermissionDenied, "refused")
	startFakeCoordinator(t, home, "proj", f)

	answers, failures := QueryPendingApprovals(context.Background())
	assert.Empty(t, answers)
	require.Len(t, failures, 1)
	assert.Equal(t, codes.PermissionDenied, status.Code(failures[0]))
}

// TestQueryPendingApprovals_UnreadableEndpoint: an endpoint file discovery
// could not read may be the one coordinator running, so with no answer it
// is a failure; once a coordinator has answered, it is not.
func TestQueryPendingApprovals_UnreadableEndpoint(t *testing.T) {
	home := testsupport.Isolate(t)
	dir := filepath.Join(home, ".ctxloom", "coord", "corrupt", "root-harp")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte("not json"), 0o600))

	answers, failures := QueryPendingApprovals(context.Background())
	assert.Empty(t, answers)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Error(), "corrupt")

	startFakeCoordinator(t, home, "proj", newFakeConsumerServer())
	answers, failures = QueryPendingApprovals(context.Background())
	assert.Len(t, answers, 1)
	assert.Empty(t, failures)
}
