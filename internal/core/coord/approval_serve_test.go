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

// askNow is an approval request from out's CURRENT turn, stamped as
// HandleRequest stamps one arriving on the wire.
func askNow(c *Coordinator, out *RunOutcome, ask engine.PermissionAsk) AgentRequest {
	return AgentRequest{Kind: ApprovalRequest{Ask: ask, Transitions: []string{"default"}, turn: c.approvals.turnOf(out.RunID)}}
}

// TestApprovalRequest_ParksAtTheRootAndAnswersTheRun: a run's approval request
// travels the wire to the coordinator, parks in the ROOT's queue stamped with
// who is asking (agent, lineage), and the human's answer is the run's reply.
func TestApprovalRequest_ParksAtTheRootAndAnswersTheRun(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "task")
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
			ToolUseId: "toolu_7", Transitions: []string{"default", "acceptEdits"}, Timeout: durationpb.New(20 * time.Minute),
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
	assert.Equal(t, []string{"default", "acceptEdits"}, p.Transitions, "the engine's transitions reach the presenter")
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
	out, _ := awaitCutoverChildIdle(t, c, sp, "task")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)

	replied := make(chan AgentReply, 1)
	go func() {
		replied <- c.serveAgentRequest(childOf(out), askNow(c, out, engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{}`)}))
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

// TestApprovalRequest_TurnEndDropsIt: the asking TURN ends while its run
// goes on — here the engine host's turn-idle boundary, as an interrupt or a
// steer produces it — and the coordinator drops the request from that event
// alone: the run gets a cancelled deny, the human's list no longer shows it,
// and the human's late answer is refused.
func TestApprovalRequest_TurnEndDropsIt(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "task")
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
			Kind: agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL, Tool: "Bash", Input: []byte(`{}`),
		}}})
		replied <- result{resp, err}
	}()
	id := awaitEvent(t, events, QueueAdded).ID

	c.mu.Lock()
	ch := c.chans[out.Harp]
	c.mu.Unlock()
	require.NotNil(t, ch)
	c.HandleEvent(ch, Event{Payload: CustomEvent{Name: CustomTurnIdle, Value: map[string]any{"stop_reason": "interrupted"}}})

	var r result
	select {
	case r = <-replied:
	case <-ctx.Done():
		t.Fatal("the asking turn ended and its request is still parked")
	}
	require.NoError(t, r.err)
	require.Zero(t, r.resp.GetStatus().GetCode(), r.resp.GetStatus().GetMessage())
	assert.False(t, r.resp.GetApproval().GetAllow())
	assert.Equal(t, agent.DeciderCancelled.String(), r.resp.GetApproval().GetDecider())
	assert.Empty(t, c.Approvals().Pending())
	require.ErrorIs(t, c.Approvals().Answer(id, ApprovalDecision{Allow: true}), ErrApprovalResolved)
}

// TestApprovalRequest_AskFromAnEndedRunNeverParks forces the ordering a run's
// end allows: the request arrives while its run lives, the run ends, and only
// then does the request reach Park. It is dropped at once as cancelled —
// never listed, journaled like any decision — and the harp's next run still
// parks its own.
func TestApprovalRequest_AskFromAnEndedRunNeverParks(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "task")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)

	// Both asked during the run's FIRST turn (turn 0): the run's end forgets
	// its turn count, so only the run's own end can refuse them.
	late := AgentRequest{Kind: ApprovalRequest{Ask: engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}}}
	later := AgentRequest{Kind: ApprovalRequest{Ask: engine.PermissionAsk{Kind: engine.AskTool, Tool: "Read"}}}
	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return c.runEnded(out.RunID) }, conformanceWait, 10*time.Millisecond)

	replied := make(chan AgentReply, 1)
	go func() { replied <- c.serveAgentRequest(childOf(out), late) }()
	var reply AgentReply
	select {
	case reply = <-replied:
	case <-ctx.Done():
		t.Fatal("a request from an ended run parked")
	}
	require.NoError(t, reply.Err)
	d, ok := reply.Result.(ApprovalDecision)
	require.True(t, ok, "the reply is the decision, got %T", reply.Result)
	assert.False(t, d.Allow)
	assert.Equal(t, agent.DeciderCancelled, d.Decider)
	assert.Empty(t, c.Approvals().Pending())
	// Park publishes before it returns: whatever it announced is buffered.
	for drained := false; !drained; {
		select {
		case ev := <-events:
			assert.NotEqual(t, QueueAdded, ev.Kind, "the ended run's request was listed")
		default:
			drained = true
		}
	}

	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	var resumed string
	require.Eventually(t, func() bool {
		resumed = currentRunID(c, out.Harp)
		return resumed != out.RunID && c.approvals.turnOf(resumed) == 1
	}, conformanceWait, 10*time.Millisecond, "the harp's next run never reached its first turn boundary")
	// Once the harp has moved on, the ended run's record is no longer its
	// current one; a request from it is still dropped.
	go func() { replied <- c.serveAgentRequest(childOf(out), later) }()
	select {
	case reply = <-replied:
	case <-ctx.Done():
		t.Fatal("a request from a superseded run parked")
	}
	d, ok = reply.Result.(ApprovalDecision)
	require.True(t, ok, "the reply is the decision, got %T", reply.Result)
	assert.Equal(t, agent.DeciderCancelled, d.Decider)
	assert.Empty(t, c.Approvals().Pending())

	next := &RunOutcome{Harp: out.Harp, RunID: resumed}
	done := make(chan AgentReply, 1)
	go func() {
		done <- c.serveAgentRequest(childOf(next), askNow(c, next, engine.PermissionAsk{Kind: engine.AskTool, Tool: "Edit"}))
	}()
	id := awaitEvent(t, events, QueueAdded).ID
	pending := c.Approvals().Pending()
	require.Len(t, pending, 1, "the next run's request parks")
	assert.Equal(t, resumed, pending[0].From.RunID)
	require.NoError(t, c.Approvals().Answer(id, ApprovalDecision{}))
	<-done
}

// grantFor parks one request from out's run and answers it allow-for-session.
func grantFor(t *testing.T, c *Coordinator, out *RunOutcome, rule string) Grant {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)
	done := make(chan AgentReply, 1)
	go func() {
		done <- c.serveAgentRequest(childOf(out), askNow(c, out, engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash"}))
	}()
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, c.Approvals().Answer(id, ApprovalDecision{Allow: true, SessionRules: []string{rule}}))
	<-done
	grants := c.Approvals().Grants(out.Harp)
	require.Len(t, grants, 1)
	return grants[0]
}

// TestApprovals_RevokeReachesTheLiveRun: a revoke on a live run is handed to
// its runner as SetGrants — the set that remains — over the real runner link,
// and only once the run has taken it is the grant gone from the record. (A
// run that refuses the set keeps the grant: TestApprovalQueue_
// RevokeRefusedByTheRunKeepsTheGrant.)
func TestApprovals_RevokeReachesTheLiveRun(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "task")
	g := grantFor(t, c, out, "Bash(ls:*)")

	require.NoError(t, c.Approvals().Revoke(out.Harp, g.ID), "the live run takes its remaining set")
	assert.Empty(t, c.Approvals().Grants(out.Harp))
}

// TestApprovals_RevokeOnAnEndedRunIsJournaledOnly: with no live run there is
// nobody to push to; the revoke is the journal's alone.
func TestApprovals_RevokeOnAnEndedRunIsJournaledOnly(t *testing.T) {
	resetStrictness(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "task")
	g := grantFor(t, c, out, "Bash(ls:*)")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough"})
	require.NoError(t, err)

	assert.Equal(t, []Grant{g}, c.Approvals().Grants(out.Harp), "a run's end does not take its harp's grants")
	require.NoError(t, c.Approvals().Revoke(out.Harp, g.ID))
	assert.Empty(t, c.Approvals().Grants(out.Harp))
}
