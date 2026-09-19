package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// FINAL IS THE COMPLETION CONTRACT, AND NOTHING ACTED ON IT. An agent files a
// SCOPE_FINAL report before finishing, and the run simply stayed live: on
// 2026-09-12 nine agents sat idle after filing FINAL, two of them holding
// containers nine hours old whose bind-mount sources pointed at worktrees that
// had since been deleted. They were released only because a human asked for a
// close-out — nothing else would have noticed, because nothing observed it.
// These tests are that observation.
//
// The chain was one connection short: recordSummary already queued the report
// to the parent, and drainAtBoundary → terminateRun already released the slot,
// revoked the credential, killed the container and queued the exit notice.
// endOnFinalReport is the connection, and it reuses the existing drain rather
// than opening a second termination path.

// finalSummary is the completion contract as an agent files it.
func finalSummary(text string) *agentcoordpb.Summary {
	return &agentcoordpb.Summary{Scope: agentcoordpb.Summary_SCOPE_FINAL, Text: text}
}

// runTerminalDetail reads a run's terminal DETAIL off the fold ("" while
// live). The cause alone cannot say WHERE a run ended, and "at its turn
// boundary" versus "between turns" is the distinction the boundary seam
// exists to make.
func runTerminalDetail(c *Coordinator, runID string) string {
	detail := ""
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			detail = r.Detail
		}
	})
	return detail
}

// currentRunID reads which run is the harp's CURRENT one — the resume key.
// A resumed harp keeps its harp and mints a FRESH run id, which is the whole
// shape of "the run ended, the session did not".
func currentRunID(c *Coordinator, harp string) string {
	id := ""
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			id = r.RunID
		}
	})
	return id
}

// endsItself asks the production predicate whether this run already tears
// itself down at its next turn boundary — used as a test PRECONDITION so the
// one-shot control below cannot pass merely because the live resume confirm
// had not been journaled yet.
func endsItself(c *Coordinator, runID string) bool {
	ok := false
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			ok = endsItselfAtBoundary(r)
		}
	})
	return ok
}

// collectOwnerMail drains the owner's mailbox for window and returns EVERY
// message in ARRIVAL ORDER. recvKind/recvWhere cannot serve an ordering
// assertion: they filter, and drop everything that does not match — including
// the very message whose position relative to the match is the claim.
func collectOwnerMail(t *testing.T, c *Coordinator, window time.Duration) []Message {
	t.Helper()
	deadline := time.Now().Add(window)
	var out []Message
	for time.Now().Before(deadline) {
		msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
		if err == nil {
			out = append(out, msgs...)
		}
	}
	return out
}

// firstIndexOfKind reports where kind first appears in msgs (-1 when absent).
func firstIndexOfKind(msgs []Message, kind string) int {
	for i, m := range msgs {
		if m.Kind == kind {
			return i
		}
	}
	return -1
}

// TestFinalReport_EndsTheRunAtItsTurnBoundary is the settling proof, in the
// shape production actually has: a child files FINAL as a TOOL CALL, so its
// turn is still running when the report lands.
//
// Both halves matter, and the first is why this cannot be done inside the
// report call. A child that files FINAL is still writing its closing message;
// terminating there truncates the agent's own last words. So the drain only
// REQUESTS the exit, and the request is honoured at the turn boundary the
// child reaches on its own.
//
// The negative half is not load-sensitive: nothing can end the turn while the
// gate is held, so "still live" is a hard invariant over the window, not a
// race the budget might hide.
func TestFinalReport_EndsTheRunAtItsTurnBoundary(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateExecuting }, conformanceWait, 10*time.Millisecond,
		"the child must be mid-turn, which is when a real agent files FINAL")

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: the deliverable"))

	// THE TURN IS NOT CUT SHORT. The exit is requested, not taken.
	assert.Never(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, 300*time.Millisecond, 20*time.Millisecond,
		"filing FINAL must not terminate a child mid-turn: its own closing message is still being written")

	// The boundary the child reaches on its own is where the request is taken.
	close(gate)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"a child that filed FINAL must have its RUN ended without anyone asking — this is the leak")
	assert.Equal(t, CauseFinalReported, runCause(c, out.RunID),
		"the terminal must name the completion contract as the reason, not a stop or a drain")
	assert.Contains(t, runTerminalDetail(c, out.RunID), "at its turn boundary",
		"the end must be taken at the child's own turn boundary — the seam drain already established")
}

// TestFinalReport_EndsAChildThatIsBetweenTurns is the OTHER arrival shape, and
// the one the nine idle agents were actually in: the report lands when the
// child is idle between turns, so there is no boundary left to wait for and
// nothing to cut short. It ends now.
func TestFinalReport_EndsAChildThatIsBetweenTurns(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: done"))

	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"an idle child that has filed FINAL must not sit holding its container")
	assert.Equal(t, CauseFinalReported, runCause(c, out.RunID))
	assert.Contains(t, runTerminalDetail(c, out.RunID), "between turns")
}

// TestProgressReport_DoesNotEndTheRun is the CONTROL that stops the two above
// from being satisfiable by ending on every report. FINAL is the completion
// contract; PROGRESS and STEP are heartbeats, and ending on one would kill a
// working agent mid-task.
func TestProgressReport_DoesNotEndTheRun(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(out.Harp, out.RunID, 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "still working",
	})

	assert.Never(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, 300*time.Millisecond, 20*time.Millisecond,
		"only FINAL is the completion contract; a heartbeat must never end a working agent")
	assert.Contains(t, c.LatestReport(out.Harp), "still working",
		"and the heartbeat is still journaled — the ending is additive, not a replacement")
}

// TestFinalReport_ParentGetsTheReportBeforeTheExitNotice pins the ORDERING
// that makes the ending legible. Both messages reach the parent, and the
// report must come FIRST: a parent that receives EXITED before the report has
// been told its child is gone with no explanation, and the report that
// explains it then arrives after the news. This is why endOnFinalReport is
// called strictly after notifyParentOfFinalReport.
//
// THE SEPARATION IS DRIVEN, NOT SAMPLED. Comparing the two messages' positions
// in one drained batch would only observe whichever won a race, and would pass
// just as happily against a coordinator that queued them the other way round
// and got lucky. Holding the child mid-turn puts a wall-clock event the test
// controls between the two: the report must be in the parent's mailbox while
// the run is still demonstrably LIVE, and the exit notice cannot exist until
// the gate releases the turn.
func TestFinalReport_ParentGetsTheReportBeforeTheExitNotice(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateExecuting }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: the finding"))

	// FIRST: the report, while the child is still running. Nothing can have
	// ended the run yet, so nothing can have queued an exit notice yet.
	msgs := collectOwnerMail(t, c, 500*time.Millisecond)
	require.GreaterOrEqual(t, firstIndexOfKind(msgs, KindReport), 0,
		"the parent must receive the FINAL report as soon as it is filed")
	require.Equal(t, -1, firstIndexOfKind(msgs, KindExited),
		"no exit notice may exist yet — the child has not reached its boundary")
	require.NotEqual(t, StateEnded, rosterState(c, out.Harp), "the run must still be live at this point")

	// THEN: the boundary, the end, and only now the exit notice.
	close(gate)
	require.NotEmpty(t, recvKind(t, c, KindExited, conformanceWait),
		"the parent must also learn the child then exited")
}

// TestFinalReport_SessionStaysResumableAfterTheRunEnds is the hazard this
// change had to design against. An agent that has filed FINAL is, by the
// project's own ruling, still RESUMABLE: a later agent_send is meant to wake
// it. ENDING THE RUN IS NOT ENDING THE SESSION, and agent_stop already draws
// exactly that line — it kills the run, not the session, and a later send
// resumes under a fresh run_id.
//
// So this asserts both sides of one event: the run really ended (a new engine
// had to be spawned, which is the container being gone), and the HARP came
// back — under a DIFFERENT run id, which is what makes each follow-up its own
// run in the journal.
func TestFinalReport_SessionStaysResumableAfterTheRunEnds(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: done"))
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	require.Equal(t, 1, sp.chatCount(), "nothing may resume while the mailbox is empty — the child is genuinely down")

	// The follow-up a parent is entitled to make after FINAL.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err, "a send to a harp that filed FINAL must be accepted, not refused as dead")

	awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp),
		"the session survives the run: a later agent_send must resume the harp")
	assert.Equal(t, 2, sp.chatCount(), "the resume must spawn a FRESH engine — proof the first one really was torn down")
	assert.NotEqual(t, out.RunID, currentRunID(c, out.Harp),
		"the resume must be a NEW run of the SAME harp; reusing the run id would mean the run never ended")
}

// TestFinalReport_OneShotBoundaryIsNotRepainted is the double-fire control. A
// driving:oneshot child ALREADY tears its engine down at every turn boundary,
// and terminateRun deliberately suppresses that terminal's parent notice
// because it fires once per TURN — spamming a parent with an "exited" per turn
// is the defect that suppression exists to prevent.
//
// onTurnIdle checks the drain's exit request BEFORE the one-shot branch, so an
// armed drain here would win and repaint an expected CauseOneShotBoundary as
// CauseFinalReported, un-suppressing that notice. The leak cannot happen to a
// child that already ends every turn, so a one-shot run is never armed.
func TestFinalReport_OneShotBoundaryIsNotRepainted(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := oneShotSpawner(func() *scriptedChat { return &scriptedChat{resumable: true, turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	// The PRECONDITION, waited for rather than assumed: this run is one that
	// ends itself at its boundary. Without the wait a fixture whose live
	// resume confirm had not landed yet would pass this test for the wrong
	// reason — it would be a persistent child, which SHOULD be armed.
	require.Eventually(t, func() bool { return endsItself(c, out.RunID) }, conformanceWait, 10*time.Millisecond,
		"the fixture must actually be a live-confirmed one-shot run")

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: task one done"))
	close(gate)

	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, CauseOneShotBoundary, runCause(c, out.RunID),
		"a one-shot child's own boundary teardown must keep its terminal; FINAL must not repaint it")
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)
}

// TestFinalReport_OwnerRunIsNeverEndedByItsOwnReport guards the case that
// would be worst to get wrong. An owner-owned run (StartOwnedRun) is
// `ctxloom run`'s OWN foreground session, and it journals ParentHarp as its
// own harp — the self-loop owner_run.go names. A guard that only checked for
// an EMPTY parent would sail straight past that and tear down the session the
// human is sitting in front of, because it reported its own work finished.
//
// There is no delegating parent whose contract this FINAL completes, so there
// is nothing to end.
func TestFinalReport_OwnerRunIsNeverEndedByItsOwnReport(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	out, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "do the thing")
	require.NoError(t, err)
	require.True(t, *started)

	// The self-loop is the trap: assert the fixture really has it, so this
	// test cannot pass merely because the parent field happened to be empty.
	var parentHarp string
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			parentHarp = r.ParentHarp
		}
	})
	require.Equal(t, ownerHarp, parentHarp, "an owner run journals its OWN harp as its parent — that is the trap being guarded")

	c.recordSummary(ownerHarp, out.RunID, 1, finalSummary("FINAL: the top-level session's own report"))

	assert.Never(t, func() bool { return rosterState(c, ownerHarp) == StateEnded }, 300*time.Millisecond, 20*time.Millisecond,
		"a top-level session must never be torn down by its own FINAL report")
}
