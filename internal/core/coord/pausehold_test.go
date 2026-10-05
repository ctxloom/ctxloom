package coord

import (
	"context"
	"fmt"
	"testing"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentInitiator is the fixture's coordinating agent: the owner, every
// child's parent.
func agentInitiator() ControlInitiator {
	return ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}
}

// TestPauseHold_OnlyTheHumanEndsTheHumansPause: a pause the human opened is
// the human's to end. An agent's resume is refused with a typed error saying
// so, and the run stays paused; the human's resume ends it.
func TestPauseHold_OnlyTheHumanEndsTheHumansPause(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)

	_, err = f.c.ControlResume(human(t), agentInitiator(), f.stranger)
	require.ErrorIs(t, err, ErrHumanPaused)
	assert.True(t, f.c.runPaused(f.runOf(t, f.stranger)), "the refused resume leaves the pause standing")

	newly, err := f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly)
}

// TestPauseHold_AnAgentsPauseEndsByThatAgentOrTheHuman: a pause an agent
// opened may be ended by that agent (or any initiator controlTarget admits
// over the child) and by the human.
func TestPauseHold_AnAgentsPauseEndsByThatAgentOrTheHuman(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), agentInitiator(), f.stranger, "waiting on a sibling")
	require.NoError(t, err)
	newly, err := f.c.ControlResume(human(t), agentInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly, "the agent ends its own pause")

	_, err = f.c.ControlPause(human(t), agentInitiator(), f.stranger, "waiting again")
	require.NoError(t, err)
	newly, err = f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly, "the human may end an agent's pause")
}

// TestHoldStop_AStoppedChildLeavesItsHold: agent_stop on a held child drops
// its harp from the hold (journaled), so a later send relaunches it normally
// — but its credential's hold is the credential's, not the child's: the run
// that comes up joins it, and resumes with the others when it releases.
func TestHoldStop_AStoppedChildLeavesItsHold(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	_, err := f.c.AgentStop(ownerIdentity(), f.sibling, "not needed", 0)
	require.NoError(t, err)
	f.awaitHold(t, f.worker)
	dropped := journaled[holdDropped](t, f.c, factHoldDropped)
	require.Len(t, dropped, 1)
	assert.Equal(t, f.sibling, dropped[0].Harp)

	_, disposition, err := f.c.peerSend(newMessageID(), ownerIdentity(), f.sibling, KindMessage, "after the stop", nil, "")
	require.NoError(t, err)
	_, prose := deliveryDisposition(StateEnded)
	assert.Equal(t, prose, disposition, "a stopped child is relaunched by a send, held or not")
	f.awaitHold(t, f.worker, f.sibling)
	var launched []string
	for _, p := range journaled[holdParked](t, f.c, factHoldParked) {
		if p.Cause == "launch" {
			launched = append(launched, p.Harp)
		}
	}
	assert.Equal(t, []string{f.sibling}, launched, "the relaunched run joins its credential's hold as it comes up")
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, f.c.awaitChildUp(ctx, f.sibling), "the relaunched run came up")
	require.Never(t, func() bool { return countChatText(f.sp, 3, "after the stop") > 0 },
		300*time.Millisecond, 10*time.Millisecond, "a run started on a held credential takes no turn until the hold releases")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	awaitChatText(t, f.sp, 3, "after the stop")
}

// TestHoldStop_AStoppedChildLeavesItsPause: agent_stop on a paused child
// ends the pause with it (journaled), so a later send relaunches it.
func TestHoldStop_AStoppedChildLeavesItsPause(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)

	_, err = f.c.AgentStop(ownerIdentity(), f.stranger, "", 0)
	require.NoError(t, err)
	assert.False(t, f.c.harpHeld(f.stranger), "the stop takes the harp out of its pause")
	assertReleased(t, f.c, "empty")

	_, disposition, err := f.c.peerSend(newMessageID(), ownerIdentity(), f.stranger, KindMessage, "after the stop", nil, "")
	require.NoError(t, err)
	_, prose := deliveryDisposition(StateEnded)
	assert.Equal(t, prose, disposition)
	awaitChatText(t, f.sp, 3, "after the stop")
}

// TestPauseHold_TheRosterShowsAPause: a paused child carries the roster's hold
// — kind human or agent, by who paused it, with no deadline and no credential
// — on both rosters, and liveness never judges it stalled; the resume clears
// both.
func TestPauseHold_TheRosterShowsAPause(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)
	_, err = f.c.ControlPause(human(t), agentInitiator(), f.sibling, "waiting on the worker")
	require.NoError(t, err)

	for harp, kind := range map[string]string{f.stranger: HoldKindHuman, f.sibling: HoldKindAgent} {
		want := &RunHold{Kind: kind}
		assert.Equal(t, want, f.holdOf(t, harp), "the wire roster shows %s's pause", harp)
		assert.Equal(t, want, f.entry(harp).Hold, "the in-process roster shows %s's pause", harp)
	}
	awaiting := map[string]bool{}
	for _, tg := range f.c.livenessTargets() {
		awaiting[tg.Harp] = tg.AwaitingApproval
	}
	assert.Equal(t, map[string]bool{f.worker: false, f.sibling: true, f.stranger: true}, awaiting)

	_, err = f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	assert.Nil(t, f.holdOf(t, f.stranger))
	assert.Nil(t, f.entry(f.stranger).Hold)
}

// TestPauseHold_ThePauseRecordsItsReason: the reason an initiator gives for a
// pause is journaled with the pause itself, so a later reader can tell a
// deliberate hold from a stall.
func TestPauseHold_ThePauseRecordsItsReason(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), agentInitiator(), f.stranger, "waiting on the worker's report")
	require.NoError(t, err)
	opened := journaled[holdOpened](t, f.c, factHoldOpened)
	require.Len(t, opened, 1)
	assert.Equal(t, "waiting on the worker's report", opened[0].Reason)
	assert.Equal(t, ownerIdentity().Harp, opened[0].By)
}

// TestHoldLaunch_ANewChildOnAHeldCredentialStartsPaused: a child launched on a
// credential whose hold is in force starts paused (StartRun's start_paused) —
// it comes up, joins the hold, and takes NO turn, not even its briefing,
// until the hold releases; the release starts its briefing.
func TestHoldLaunch_ANewChildOnAHeldCredentialStartsPaused(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	out, err := f.c.AgentRun(context.Background(), ownerIdentity(), "sibling", "the new child's briefing", "", "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, f.c.awaitChildUp(ctx, out.Harp), "a run started paused still comes up")
	hold := f.awaitHold(t, f.worker, f.sibling, out.Harp)
	assert.Equal(t, string(hold.Kind), f.holdOf(t, out.Harp).Kind, "the roster shows the new child held")
	assert.Equal(t, StateIdle, f.state(out.Harp), "a run started paused reads idle, like every held run")
	f.c.mu.Lock()
	slot := f.c.attach[out.RunID].slot
	f.c.mu.Unlock()
	assert.Equal(t, slotFree, slot, "a run started paused holds no concurrency slot while it waits")
	require.Never(t, func() bool { return countChatText(f.sp, 3, "the new child's briefing") > 0 },
		300*time.Millisecond, 10*time.Millisecond, "a run started on a held credential takes no turn, not even its briefing")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	awaitChatText(t, f.sp, 3, "the new child's briefing")
}

// TestControlPause_AnUnansweredPauseKeepsItsHold: the runner takes the pause
// but its answer never reaches the coordinator before the caller gives up.
// The runner is paused, so the journaled hold must stand — the steer
// disposition reads it, and a later resume must be able to release it. The
// interleaving is forced: the runner's handler blocks on the pause until the
// caller's context has ended, then installs the gate.
func TestControlPause_AnUnansweredPauseKeepsItsHold(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(t, 0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	entered, release := make(chan struct{}), make(chan struct{})
	sp.mu.Lock()
	sp.refuse = func(req *agentcoordpb.RunnerRequest) error {
		if req.GetPauseRun() != nil {
			close(entered)
			<-release
		}
		return nil
	}
	sp.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ControlPause(ctx, humanInitiator(), out.Harp, "human is reviewing")
		done <- err
	}()
	<-entered
	cancel()
	err := <-done
	close(release)

	require.ErrorIs(t, err, context.Canceled, "the caller still hears that it got no answer")
	assert.True(t, c.harpHeld(out.Harp), "a pause the runner may have taken keeps its journaled hold")
	assert.Empty(t, journaled[holdReleased](t, c, factHoldReleased), "nothing released the hold")

	rctx, rcancel := context.WithTimeout(context.Background(), conformanceWait)
	defer rcancel()
	_, err = c.ControlResume(rctx, humanInitiator(), out.Harp)
	require.NoError(t, err)
	assert.False(t, c.harpHeld(out.Harp), "the kept hold is the resume's to release")
}

// TestControlPause_ARefusedPauseDropsItsHold: a runner that answers the pause
// with a refusal did not pause, so the hold journaled before the send is
// dropped with the error.
func TestControlPause_ARefusedPauseDropsItsHold(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(t, 0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	sp.mu.Lock()
	sp.refuse = refuseOnly(func(req *agentcoordpb.RunnerRequest) bool { return req.GetPauseRun() != nil }, remedialRefusal())
	sp.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.ControlPause(ctx, humanInitiator(), out.Harp, "human is reviewing")
	require.Error(t, err)
	assert.False(t, c.harpHeld(out.Harp), "a refused pause leaves no hold behind")
}

// TestRequestMayHaveLanded: only a request that reached the session's send
// queue and then lost its answer is ambiguous; a refusal, and a request that
// never left the coordinator, are definitive. Wrapped as sendRunnerControl
// wraps them.
func TestRequestMayHaveLanded(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"the wait ran out after the send":    {fmt.Errorf("pause h: %w", fmt.Errorf("%w: %w", errRunnerUnanswered, context.DeadlineExceeded)), true},
		"the session ended before answering": {fmt.Errorf("pause h refused: %w", ErrRunnerSessionEnded), true},
		"the runner refused":                 {fmt.Errorf("pause h refused: %w", errLaunchRemedyCause), false},
		"no runner request could reach it":   {fmt.Errorf("pause: %w", ErrCapabilityUnavailable), false},
		"the wait ran out before the send":   {fmt.Errorf("pause h: %w", context.DeadlineExceeded), false},
	} {
		assert.Equal(t, tc.want, requestMayHaveLanded(tc.err), name)
	}
}
