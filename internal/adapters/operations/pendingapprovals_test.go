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

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestDiscoverCoordinators_NamesTheProject: every live coordinator on the
// host is found, each with the project it serves.
func TestDiscoverCoordinators_NamesTheProject(t *testing.T) {
	home := testsupport.Isolate(t)
	startFakeCoordinator(t, home, "proj", newFakeConsumerServer())

	cs, skipped := DiscoverCoordinators()
	require.Empty(t, skipped)
	require.Len(t, cs, 1)
	assert.Equal(t, fakeProjectDir("proj"), cs[0].ProjectDir)
	assert.NotEmpty(t, cs[0].URL)
}

// TestDiscoverCoordinators_ReportsUnreadableEndpointFiles: an endpoint file
// discovery could not read is reported, not silently dropped — it may be the
// one coordinator that is running.
func TestDiscoverCoordinators_ReportsUnreadableEndpointFiles(t *testing.T) {
	home := testsupport.Isolate(t)
	dir := filepath.Join(home, ".ctxloom", "coord", "corrupt", "root-harp")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte("not json"), 0o600))

	cs, skipped := DiscoverCoordinators()
	assert.Empty(t, cs)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Error(), "corrupt")
}

// TestQueryPendingApprovals_ReadsTheQueue: a coordinator answers with its
// parked requests, every field intact, over the credential its
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
	cs, _ := DiscoverCoordinators()
	require.Len(t, cs, 1)

	got, err := QueryPendingApprovals(context.Background(), cs[0])
	require.NoError(t, err)
	assert.Equal(t, "/proj", got.GetProjectDir())
	require.Len(t, got.GetPending(), 1)
	assert.Equal(t, "Bash: make", got.GetPending()[0].GetSummary())
	assert.Equal(t, []string{"root", "child-a"}, got.GetPending()[0].GetLineage())
}

// TestQueryPendingApprovals_StatusIsKept: the error keeps the gRPC status, so
// a caller can tell a coordinator nobody answers for (Unavailable) from one
// that answered with a refusal.
func TestQueryPendingApprovals_StatusIsKept(t *testing.T) {
	dead := Coordinator{Endpoint: discover.Endpoint{URL: discover.LoopbackURL(1), Cred: "stale"}} // port 1: nothing listens
	_, err := QueryPendingApprovals(context.Background(), dead)
	require.Error(t, err)
	assert.Contains(t, err.Error(), dead.URL)
	assert.Equal(t, codes.Unavailable, status.Code(err))

	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvalsErr = status.Error(codes.PermissionDenied, "refused")
	startFakeCoordinator(t, home, "proj", f)
	cs, _ := DiscoverCoordinators()
	require.Len(t, cs, 1)
	_, err = QueryPendingApprovals(context.Background(), cs[0])
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}
