package coord

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Hermetic conformance tests for the runtime coordinator, adapting the
// retired agentbus/orchestrator suite onto the coord.Coordinator public API
// with a fake spawner (no config, no engines, no containers).

const conformanceWait = 5 * time.Second

func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// ownerIdentity is the coordinating session's identity (depth 0).
func ownerIdentity() Identity { return Identity{Harp: "coordinator-harp", Depth: 0} }

// TestAgentRun_HonorsAgentIntent pins intent honoring: the composed context
// leads the first turn ahead of the briefing, the declared headless-safe
// permission enum resolves, the runtime axis is reported, and the child's
// ambient identity + coordinator reach-back trio reach the engine env — with
// the credential present ONLY in env, never surfaced elsewhere.
func TestAgentRun_HonorsAgentIntent(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"researcher": {perm: "bypass", profiles: []string{"p1"}},
	}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "researcher", "find the thing", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, out.Harp)
	assert.Equal(t, "fast", out.Engine)
	assert.Equal(t, []string{"p1"}, out.Profiles)
	assert.Equal(t, "host", out.Runtime)
	assert.False(t, out.Queued)

	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	first := sp.engine(0).recordedTexts()[0]
	assert.Contains(t, first, "FRAG-ONE", "composed context leads the first turn")
	assert.Contains(t, first, "find the thing", "the briefing is the first turn")

	env := sp.engine(0).env()
	assert.Equal(t, out.Harp, env["CTXLOOM_SESSION_HARP"], "ambient identity, never client-claimed")
	assert.Empty(t, env[EnvCoordCred], "the ENGINE env never carries the credential (the runner is the one holder)")
	renv := sp.engine(0).runnerEnv()
	assert.NotEmpty(t, renv[EnvCoordURL], "coordinator reach-back URL rides the runner spawn env")
	assert.NotEmpty(t, renv[EnvCoordCred], "credential rides ONLY the runner spawn env")
	assert.Equal(t, out.RunID, renv[EnvRunID], "run id correlates the runner")
}

// TestAgentRun_UnknownAgentIsHardError pins run --agent parity.
func TestAgentRun_UnknownAgentIsHardError(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	_, err := c.AgentRun(context.Background(), ownerIdentity(), "ghost", "boo", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent "ghost" not found`)
}

// TestAgentRun_D3RefusesNonHeadless pins the D3 gate: an agent with no
// headless-safe permission is refused with the typed finding, and never
// launches.
func TestAgentRun_D3RefusesNonHeadless(t *testing.T) {
	for _, tc := range []struct{ name, perm, reason string }{
		{"absent enum", "", "declares no permissions"},
		{"non-headless enum", "default", "not headless-safe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			sp := newFakeSpawner(map[string]fakeAgent{"loose": {perm: tc.perm, profiles: []string{"p1"}}}, nil)
			c := newTestCoordinator(t, sp, nil)
			_, err := c.AgentRun(context.Background(), ownerIdentity(), "loose", "go", "", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
			assert.Contains(t, err.Error(), `set permissions: plan|bypass on agent "loose"`)
			assert.Equal(t, 0, sp.spawnCount(), "a refused agent never launches")
		})
	}
}

// TestAgentRun_D3DegradedDowngradesToPlan pins the degraded downgrade.
func TestAgentRun_D3DegradedDowngradesToPlan(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	sp := newFakeSpawner(map[string]fakeAgent{"loose": {profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "loose", "go", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Degraded)
	assert.Contains(t, out.Degraded[0], "plan")

	require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(sp.engine(0).recordedTexts()) == 1 }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, agent.PermissionPlan, sp.lastPerm(), "degraded narrows a child to the most restrictive headless-safe posture")
}

// TestAgentRun_QueuePastCap pins D4/D5: the second spawn past cap=1 enqueues
// and starts when the first child's turn ends.
func TestAgentRun_QueuePastCap(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinatorCap(t, sp, nil, 1) // pin cap=1: this test exercises D4 QUEUEING past the cap, not the (now-configurable) default cap value

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	assert.False(t, first.Queued)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task two", "", "")
	require.NoError(t, err)
	assert.True(t, second.Queued, "past the cap the spawn enqueues, never errors")
	assert.NotEqual(t, first.Harp, second.Harp)
	assert.Equal(t, 1, sp.spawnCount(), "the queued child must not launch while the slot is held")

	gate <- struct{}{} // finish child 1's turn → idle → slot released
	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		e := sp.engine(1)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Contains(t, sp.engine(1).recordedTexts()[0], "task two")
}

// TestAgentRun_GrandchildAllowed pins that raising delegation.depth (here, 2)
// re-enables one further level with no other code change: a depth-1
// delegated child MAY spawn a depth-2 grandchild, through the exact same
// AgentRun/enqueueRun path a depth-0 session owner uses (the
// credential-derived depth guard is generic in the caller's depth — nothing
// here is grandchild-specific). At the BUILT-IN default cap (1) this same
// caller would be refused — see TestAgentRun_DepthAtCapRefused.
func TestAgentRun_GrandchildAllowed(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	childCaller := Identity{Harp: "some-child", RunID: "run-child", Depth: 1}
	out, err := c.AgentRun(context.Background(), childCaller, "worker", "go deeper", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)

	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			cp := *r
			rec = &cp
		}
	})
	require.NotNil(t, rec)
	assert.Equal(t, "some-child", rec.ParentHarp)
	assert.Equal(t, "run-child", rec.ParentRunID, "the grandchild's durable lineage names the SPAWNING run, not just its harp")
	assert.Equal(t, 2, rec.Depth)
}

// TestAgentRun_GreatGrandchildRefused pins the guard's floor at a RAISED cap
// (2): a depth-2 grandchild — already at the cap — still may not spawn
// further, exercising the boundary at a cap other than the built-in default
// so this test does not collapse into TestAgentRun_DepthAtCapRefused.
func TestAgentRun_GreatGrandchildRefused(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	grandchildCaller := Identity{Harp: "some-grandchild", RunID: "run-grandchild", Depth: 2}
	_, err := c.AgentRun(context.Background(), grandchildCaller, "worker", "go deeper still", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum delegation depth")
	assert.Equal(t, 0, sp.spawnCount())
}

// TestAgentRun_DepthAtCapRefused pins the BUILT-IN default cap (1): a
// depth-1 delegated child — the first generation below the session owner —
// may not itself spawn. This is the flat-fan-out regression this whole
// design exists to prevent: a top-level session (depth 0) must still be
// able to delegate at the default cap, but its children must not be able to
// delegate further.
func TestAgentRun_DepthAtCapRefused(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil) // built-in default depth cap (1)
	childCaller := Identity{Harp: "some-child", RunID: "run-child", Depth: 1}
	_, err := c.AgentRun(context.Background(), childCaller, "worker", "go deeper", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum delegation depth")
	assert.Equal(t, 0, sp.spawnCount())

	ownerCaller := Identity{Harp: "owner", Depth: 0}
	_, err = c.AgentRun(context.Background(), ownerCaller, "worker", "go deeper", "", "")
	require.NoError(t, err, "the session owner (depth 0) must still be able to delegate at the default cap")
}

// TestAgentSend_MidTurnQueuesForBoundary_FIFO pins §6a mid-turn delivery and
// per-sender FIFO ordering.
func TestAgentSend_MidTurnQueuesForBoundary_FIFO(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	for i := 1; i <= 3; i++ {
		disp, serr := c.AgentSend(ownerIdentity(), out.Harp, "", fmt.Sprintf("follow-up %d", i), nil, "")
		require.NoError(t, serr)
		assert.Contains(t, disp, "queued")
	}

	e := sp.engine(0)
	for turn := 2; turn <= 4; turn++ {
		gate <- struct{}{}
		// The body arrives inside the provenance frame every mailbox delivery
		// carries (frameCoordinatorDelivery), so ORDER is what this asserts:
		// each turn carries its own follow-up and no later one.
		want := fmt.Sprintf("follow-up %d", turn-1)
		require.Eventually(t, func() bool {
			texts := e.recordedTexts()
			return len(texts) == turn && strings.HasSuffix(texts[turn-1], want)
		}, conformanceWait, 10*time.Millisecond, "turn %d should carry %q", turn, want)
	}
}

// TestAgentSend_UnknownRecipient pins the coordinator-side routing errors.
func TestAgentSend_UnknownRecipient(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	_, err := c.AgentSend(ownerIdentity(), "nonexistent-harp", "", "hello", nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown recipient")

	_, err = c.AgentSend(ownerIdentity(), ParentAddress, "", "hello", nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no parent")
}

// TestChildSend_ParentOnly pins hub-and-spoke: a delegated child may address
// only its parent; a sibling is rejected, the parent lands in the owner's
// mailbox.
func TestChildSend_ParentOnly(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "report back", "", "")
	require.NoError(t, err)
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}

	_, err = c.AgentSend(child, ParentAddress, "result", "finding A", nil, "")
	require.NoError(t, err)
	_, err = c.AgentSend(child, "some-sibling-harp", "", "psst", nil, "")
	require.ErrorIs(t, err, ErrPeerRouting)

	msgs := recvBody(t, c, "finding A", time.Second)
	require.Len(t, msgs, 1)
	assert.Equal(t, out.Harp, msgs[0].From)
}

// TestAgentRecv_TimeoutIsTypedFailure pins the recv timeout contract.
func TestAgentRecv_TimeoutIsTypedFailure(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	_, err := c.AgentRecv(context.Background(), ownerIdentity(), 20*time.Millisecond)
	require.ErrorIs(t, err, ErrRecvTimeout)
}

// TestAgentSend_ResumesEndedChild pins §6a resume delivery.
func TestAgentSend_ResumesEndedChild(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{endAfterTurns: 1} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	// Drain the synthesized terminal notice so the resume assertion is clean.
	_, _ = c.AgentRecv(context.Background(), ownerIdentity(), 20*time.Millisecond)

	disp, err := c.AgentSend(ownerIdentity(), out.Harp, "", "one more thing", nil, "")
	require.NoError(t, err)
	assert.Contains(t, disp, "resuming")

	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		e := sp.engine(1)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	resumedFirst := sp.engine(1).recordedTexts()[0]
	assert.Contains(t, resumedFirst, "one more thing", "the message is the resumed session's first turn")
	assert.Contains(t, resumedFirst, "FRAG-ONE", "the agent's composed context primes the resume")
}

// rosterState reads one harp's state off the roster snapshot.
func rosterState(c *Coordinator, harp string) string {
	for _, e := range c.Roster() {
		if e.Harp == harp {
			return e.State
		}
	}
	return ""
}

// TestRoster_TracksChildStates pins the roster across the child lifecycle.
func TestRoster_TracksChildStates(t *testing.T) {
	resetStrictness(t)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var spawned int
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	sp.next = func() *fakeEngine {
		e := &fakeEngine{turnGate: gates[spawned%len(gates)]}
		spawned++
		return e
	}
	c := newTestCoordinator(t, sp, nil)

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, StateExecuting, rosterState(c, first.Harp))

	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task two", "", "")
	require.NoError(t, err)
	assert.Equal(t, StateQueued, rosterState(c, second.Harp))

	for _, e := range c.Roster() {
		assert.Equal(t, "worker", e.Agent)
		assert.Equal(t, "coordinator-harp", e.Parent)
		assert.NotZero(t, e.LastActivityUnix)
	}

	// Child 1 parks in agent_recv → roster shows parked, and its slot frees.
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = c.AgentRecv(context.Background(), Identity{Harp: first.Harp, RunID: first.RunID, Depth: 1}, conformanceWait)
	}()
	require.Eventually(t, func() bool { return rosterState(c, first.Harp) == StateParked }, conformanceWait, 10*time.Millisecond)

	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		e := sp.engine(1)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	gates[1] <- struct{}{}
	require.Eventually(t, func() bool { return rosterState(c, second.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.AgentSend(ownerIdentity(), first.Harp, KindResult, "42", nil, "")
	require.NoError(t, err)
	<-recvDone
	gates[0] <- struct{}{}
	require.Eventually(t, func() bool { return rosterState(c, first.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
}

// TestParkedRecvYieldsSlot pins the §6a slot yield: a parked child releases
// its slot (the queued child starts) and the answer completes the recv only
// after a slot re-acquires.
func TestParkedRecvYieldsSlot(t *testing.T) {
	resetStrictness(t)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var spawned int
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	sp.next = func() *fakeEngine {
		e := &fakeEngine{turnGate: gates[spawned%len(gates)]}
		spawned++
		return e
	}
	c := newTestCoordinatorCap(t, sp, nil, 1) // pin cap=1: this test exercises the D4 slot yield past the cap, not the (now-configurable) default cap value

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "ask a question", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "other work", "", "")
	require.NoError(t, err)
	require.True(t, second.Queued)
	assert.Equal(t, 1, sp.spawnCount())

	recvDone := make(chan []Message, 1)
	go func() {
		msgs, _ := c.AgentRecv(context.Background(), Identity{Harp: first.Harp, RunID: first.RunID, Depth: 1}, conformanceWait)
		recvDone <- msgs
	}()

	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"a parked child must not block the queue")

	disp, err := c.AgentSend(ownerIdentity(), first.Harp, KindResult, "42", nil, "")
	require.NoError(t, err)
	assert.Contains(t, disp, "waiting agent_recv")
	select {
	case <-recvDone:
		t.Fatal("parked recv completed while the slot was still held by child 2")
	case <-time.After(150 * time.Millisecond):
	}

	gates[1] <- struct{}{} // finish child 2's turn → slot frees → child 1 unparks
	select {
	case msgs := <-recvDone:
		require.Len(t, msgs, 1)
		assert.Equal(t, "42", msgs[0].Body)
	case <-time.After(conformanceWait):
		t.Fatal("parked recv never completed after the slot freed")
	}
}

// TestAgentStop_FreesSlot pins agent_stop: the child ends, the slot frees, the
// queued child starts, and the terminal notice reaches the parent.
func TestAgentStop_FreesSlot(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinatorCap(t, sp, nil, 1) // pin cap=1: this test exercises D4 QUEUEING past the cap, not the (now-configurable) default cap value

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task two", "", "")
	require.NoError(t, err)
	require.True(t, second.Queued)

	disp, err := c.AgentStop(ownerIdentity(), first.Harp)
	require.NoError(t, err)
	assert.Contains(t, disp, "freed")
	assert.Equal(t, StateEnded, rosterState(c, first.Harp))

	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"stopping the running child frees the slot for the queued one")

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	assert.Equal(t, KindExited, msgs[0].Kind)
}

// TestInject_DeliveryModes pins the S2 inject verb across delivery-by-state:
// queued mid-turn (with the O3 mirror), and the typed refusal for a foreign
// harp.
func TestInject_DeliveryModes(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	mode, err := c.Inject(out.Harp, "mid-turn note")
	require.NoError(t, err)
	assert.Equal(t, "queued", mode)

	// The O3 mirror reached the parent.
	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, KindUserInjected, msgs[0].Kind)
	assert.Equal(t, out.Harp, msgs[0].From)

	gate <- struct{}{} // finish turn 1 → boundary drains the injection as turn 2
	require.Eventually(t, func() bool {
		texts := sp.engine(0).recordedTexts()
		// Provenance-framed (frameCoordinatorDelivery): the injected body is the
		// turn's content, the header names the user as its sender.
		return len(texts) == 2 && texts[1] == frameCoordinatorDelivery(UserSender, "", "mid-turn note")
	}, conformanceWait, 10*time.Millisecond)

	_, err = c.Inject("foreign-session-harp", "hello?")
	require.ErrorIs(t, err, ErrNotInjectable, "a non-child target is the typed refusal, not just any error")
}

// TestInject_MirrorDigestTruncatesLongText pins the O3 mirror's 120-rune
// digest bound (coordinator.go's injectDigestRunes/injectDigest, :560): a
// digest over the bound is truncated AT A RUNE boundary (not bytes — the
// probe text below is multi-byte per rune) with the "… (N chars total)"
// suffix; the full text still reaches the CHILD verbatim (queueMail's first
// call takes the untruncated body — only the parent-facing mirror digests).
func TestInject_MirrorDigestTruncatesLongText(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	long := strings.Repeat("π", 130) // 130 RUNES (2 bytes each) — proves rune counting, not byte counting
	_, err = c.Inject(out.Harp, long)
	require.NoError(t, err)

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, KindUserInjected, msgs[0].Kind)
	wantDigest := string([]rune(long)[:120]) + "… (130 chars total)"
	assert.Equal(t, wantDigest, msgs[0].Body)

	gate <- struct{}{} // release the held turn so the child's turnGate goroutine doesn't leak past the test
	require.Eventually(t, func() bool {
		texts := sp.engine(0).recordedTexts()
		return len(texts) == 2 && strings.HasSuffix(texts[1], long)
	}, conformanceWait, 10*time.Millisecond, "the child itself receives the injection VERBATIM (inside its provenance frame), undigested")
}

// TestInject_WakesIdleChildAsNewTurn pins the DeliveryNewTurn mode: injecting
// into a child idling at a turn boundary (no queued mail) wakes it into a
// fresh turn carrying the injected text (driveQueued's StateIdle branch,
// coordinator.go:551).
func TestInject_WakesIdleChildAsNewTurn(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	mode, err := c.Inject(out.Harp, "wake up")
	require.NoError(t, err)
	assert.Equal(t, DeliveryNewTurn, mode)

	require.Eventually(t, func() bool {
		texts := sp.engine(0).recordedTexts()
		return len(texts) == 2 && texts[1] == frameCoordinatorDelivery(UserSender, "", "wake up")
	}, conformanceWait, 10*time.Millisecond)

	// The O3 mirror fires for every delivery mode, this one included.
	msgs := recvKind(t, c, KindUserInjected, time.Second)
	require.Len(t, msgs, 1)
}

// TestInject_ResumesEndedChild pins the DeliveryResumed mode: injecting into
// a child whose session already ended relaunches it (coordinator.go's
// driveQueued StateEnded branch -> resumeChild), the injected text riding
// the resumed session's first turn — the same resume mechanics
// TestAgentSend_ResumesEndedChild pins for agent_send, now proven for the
// user-injection seam.
func TestInject_ResumesEndedChild(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{endAfterTurns: 1} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	// Drain the synthesized terminal notice (KindExited) so the mirror
	// assertion below is clean. terminateRun journals the Ended state before
	// it queues the exited notice (children.go:691), so the two Evenutallys
	// above and below close a real — if narrow — window; retry-drain rather
	// than a single fixed-timeout recv (a flat 20ms drain raced this and
	// occasionally left the exited notice to arrive bundled with the mirror).
	require.Eventually(t, func() bool {
		msgs, _ := c.AgentRecv(context.Background(), ownerIdentity(), 20*time.Millisecond)
		for _, m := range msgs {
			if m.Kind == KindExited {
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the ended child's terminal notice never reached the parent")

	mode, err := c.Inject(out.Harp, "one more thing")
	require.NoError(t, err)
	assert.Equal(t, DeliveryResumed, mode)

	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		e := sp.engine(1)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	resumedFirst := sp.engine(1).recordedTexts()[0]
	assert.Contains(t, resumedFirst, "one more thing", "the injected text is the resumed session's first turn")
	assert.Contains(t, resumedFirst, "FRAG-ONE", "the agent's composed context primes the resume")

	// The resumed engine shares the spawner's endAfterTurns:1 script too (it
	// completes its own one turn and exits), so a SECOND KindExited notice
	// may land alongside the O3 mirror here — search for the mirror rather
	// than asserting an exact count.
	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	var mirrors []Message
	for _, m := range msgs {
		if m.Kind == KindUserInjected {
			mirrors = append(mirrors, m)
		}
	}
	require.Len(t, mirrors, 1, "exactly one O3 mirror for this one injection")
	assert.Equal(t, out.Harp, mirrors[0].From)
}

// TestInject_CompletesParkedRecvWithUserSenderIdentity pins two invariants
// together: the child mail Inject queues is sent from UserSender ("user"),
// distinguishable from its parent's own messages — and when the target
// itself is parked in its OWN agent_recv (independent of the coordinator-
// driven turn loop — the same parking TestParkedRecvYieldsSlot/
// TestAgentRun_TwoChildrenAndSlotHandoff exercise), Inject completes that
// parked receive directly (DeliveryCompletedRecv), not merely a turn-
// boundary queue.
func TestInject_CompletesParkedRecvWithUserSenderIdentity(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	recvDone := make(chan []Message, 1)
	go func() {
		msgs, _ := c.AgentRecv(context.Background(), Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}, conformanceWait)
		recvDone <- msgs
	}()
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateParked }, conformanceWait, 10*time.Millisecond)

	mode, err := c.Inject(out.Harp, "direct note")
	require.NoError(t, err)
	assert.Equal(t, DeliveryCompletedRecv, mode)

	select {
	case msgs := <-recvDone:
		require.Len(t, msgs, 1)
		assert.Equal(t, UserSender, msgs[0].From, "the child sees the injection came from the user, not its parent")
		assert.Equal(t, "direct note", msgs[0].Body)
	case <-time.After(conformanceWait):
		t.Fatal("the parked recv never completed")
	}

	// The O3 mirror still reaches the parent even on the completed-recv path.
	pmsgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.Len(t, pmsgs, 1)
	assert.Equal(t, KindUserInjected, pmsgs[0].Kind)

	gate <- struct{}{} // release the held turn so its goroutine doesn't leak past the test
}
