package coord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func childOf(out *RunOutcome) Identity { return Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1} }

// TestApprovalRequest_ParksAtTheRootAndAnswersTheRun: a run's approval request
// travels the wire to the coordinator, parks in the ROOT's queue stamped with
// who is asking (agent, lineage), and the human's answer is the run's reply.
func TestApprovalRequest_ParksAtTheRootAndAnswersTheRun(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "task")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)

	type result struct {
		resp *agentcoordpb.CoordinatorResponse
		err  error
	}
	replied := make(chan result, 1)
	go func() {
		resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Approval{Approval: &agentcoordpb.ApprovalRequest{
			Kind: agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL, Tool: "Bash", Input: []byte(`{"command":"make"}`),
			ToolUseId: "toolu_7", Ceiling: "acceptEdits", Timeout: durationpb.New(20 * time.Minute),
		}}})
		replied <- result{resp, err}
	}()

	id := awaitEvent(t, events, QueueAdded).ID
	pending := c.Approvals().Pending()
	require.Len(t, pending, 1)
	p := pending[0]
	assert.Equal(t, id, p.ID)
	assert.Equal(t, ApprovalTool, p.Kind)
	assert.Equal(t, out.Harp, p.From.Harp)
	assert.Equal(t, "worker", p.Agent)
	assert.Equal(t, []string{ownerIdentity().Harp, out.Harp}, p.Lineage, "root → … → the asking harp")
	assert.Equal(t, "Bash", p.Ask.Tool)
	assert.JSONEq(t, `{"command":"make"}`, string(p.Ask.Input))
	assert.Equal(t, engine.PermissionAcceptEdits, p.Ceiling)
	assert.Equal(t, 20*time.Minute, p.Deadline.Sub(p.Since))

	require.NoError(t, c.Approvals().Answer(id, ApprovalDecision{Allow: true, Message: "fine"}))
	r := <-replied
	require.NoError(t, r.err)
	require.Zero(t, r.resp.GetStatus().GetCode(), r.resp.GetStatus().GetMessage())
	assert.True(t, r.resp.GetApproval().GetAllow())
	assert.Equal(t, "human", r.resp.GetApproval().GetDecider())
	assert.Equal(t, "fine", r.resp.GetApproval().GetMessage())
}

// TestApprovalRequest_RunEndWithdrawsIt: a request outlives nothing — when
// the asking run ends, its parked request is denied as cancelled at once
// rather than holding the human's attention until its timeout.
func TestApprovalRequest_RunEndWithdrawsIt(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "task")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)

	replied := make(chan AgentReply, 1)
	go func() {
		replied <- c.serveAgentRequest(childOf(out), AgentRequest{Kind: ApprovalRequest{
			Ask: engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{}`)}, Ceiling: engine.PermissionDefault,
		}})
	}()
	awaitEvent(t, events, QueueAdded)

	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough"})
	require.NoError(t, err)
	var reply AgentReply
	select {
	case reply = <-replied:
	case <-ctx.Done():
		t.Fatal("the run ended and its request is still parked")
	}
	require.NoError(t, reply.Err)
	d, ok := reply.Result.(ApprovalDecision)
	require.True(t, ok, "the reply is the decision, got %T", reply.Result)
	assert.False(t, d.Allow)
	assert.Equal(t, agent.DeciderCancelled, d.Decider)
	assert.Empty(t, c.Approvals().Pending())
}

// grantFor parks one request from out's run and answers it allow-for-session.
func grantFor(t *testing.T, c *Coordinator, out *RunOutcome, rule string) Grant {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)
	done := make(chan AgentReply, 1)
	go func() {
		done <- c.serveAgentRequest(childOf(out), AgentRequest{Kind: ApprovalRequest{
			Ask: engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}, Ceiling: engine.PermissionDefault,
		}})
	}()
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, c.Approvals().Answer(id, ApprovalDecision{Allow: true, SessionRules: []string{rule}}))
	<-done
	grants := c.Approvals().Grants(out.Harp)
	require.Len(t, grants, 1)
	return grants[0]
}

// TestApprovals_RevokeGoesToTheLiveRunFirst: a revoke is handed to the run
// before it is journaled, so a run that does not take it keeps the grant — the
// record never claims a rule is gone while the run still applies it.
func TestApprovals_RevokeGoesToTheLiveRunFirst(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "task")
	g := grantFor(t, c, out, "Bash(ls:*)")

	err := c.Approvals().Revoke(out.Harp, g.ID)
	require.Error(t, err, "this runner does not take SetGrants, and says so")
	assert.Contains(t, err.Error(), "not offered by this runner")
	assert.Equal(t, []Grant{g}, c.Approvals().Grants(out.Harp))
}

// TestApprovals_RevokeOnAnEndedRunIsJournaledOnly: with no live run there is
// nobody to push to; the revoke is the journal's alone.
func TestApprovals_RevokeOnAnEndedRunIsJournaledOnly(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "task")
	g := grantFor(t, c, out, "Bash(ls:*)")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough"})
	require.NoError(t, err)

	assert.Equal(t, []Grant{g}, c.Approvals().Grants(out.Harp), "a run's end does not take its harp's grants")
	require.NoError(t, c.Approvals().Revoke(out.Harp, g.ID))
	assert.Empty(t, c.Approvals().Grants(out.Harp))
}
