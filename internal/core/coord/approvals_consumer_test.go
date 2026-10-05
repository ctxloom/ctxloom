package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// TestConsumerService_PendingApprovals_ListsAParkedRequestReadOnly: a
// request a child parks at the root is visible to a viewer holding only the
// consumer credential — who asked, through which lineage, the summary, the
// times, the project — and reading it changes nothing: the request is still
// parked, still the human's to answer, and gone from the list once answered.
func TestConsumerService_PendingApprovals_ListsAParkedRequestReadOnly(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(t, 0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "task")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)

	replied := make(chan error, 1)
	go func() {
		_, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Approval{Approval: &agentcoordpb.ApprovalRequest{
			Kind: agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL, Tool: "Bash", Input: []byte(`{"command":"make"}`),
			Transitions: []*agentcoordpb.PostureTransition{{Posture: "default", Label: "default", Default: true}}, Timeout: durationpb.New(20 * time.Minute),
		}}})
		replied <- err
	}()
	id := awaitEvent(t, events, QueueAdded).ID

	client, _ := dialConsumer(t, c.LoopbackURL(), c.consumerCreds.token())
	got, err := client.PendingApprovals(ctx, &agentcoordpb.PendingApprovalsRequest{})
	require.NoError(t, err)
	assert.Equal(t, c.projectDir, got.GetProjectDir())
	require.Len(t, got.GetPending(), 1)
	p := got.GetPending()[0]
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL, p.GetKind())
	assert.Equal(t, out.Harp, p.GetHarp())
	assert.Equal(t, "worker", p.GetAgent())
	assert.Equal(t, []string{ownerIdentity().Harp, out.Harp}, p.GetLineage())
	assert.Equal(t, "Bash: make", p.GetSummary())
	assert.Equal(t, 20*time.Minute, p.GetDeadline().AsTime().Sub(p.GetSince().AsTime()))

	pending := c.Approvals().Pending()
	require.Len(t, pending, 1, "reading the list must not resolve the request")
	assert.Equal(t, id, pending[0].ID)
	require.NoError(t, c.Approvals().Answer(id, ApprovalDecision{Allow: true}), "the request is still the human's to answer")
	require.NoError(t, <-replied)

	after, err := client.PendingApprovals(ctx, &agentcoordpb.PendingApprovalsRequest{})
	require.NoError(t, err)
	assert.Empty(t, after.GetPending())
}

// TestConsumerService_PendingApprovals_Empty: a coordinator with nothing
// parked answers an empty list naming its project, not an error.
func TestConsumerService_PendingApprovals_Empty(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(t, nil, nil), nil)
	client, _ := dialConsumer(t, c.LoopbackURL(), c.consumerCreds.token())
	got, err := client.PendingApprovals(context.Background(), &agentcoordpb.PendingApprovalsRequest{})
	require.NoError(t, err)
	assert.Empty(t, got.GetPending())
	assert.Equal(t, c.projectDir, got.GetProjectDir())
}
