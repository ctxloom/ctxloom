package coord

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/liveness"
)

// The coordinator-side half of the three-direction proof: the SAME reproduced
// incident, reached through the real fold projection rather than a
// hand-assembled liveness.Target, so a bug in the adapter (a harp that never
// reaches the monitor, a transcript path that resolves to nothing, an approval
// park that fails to suppress) is caught here rather than by nothing.

func livenessTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// onlyFixtureTranscript unlinks the transcript the child's own runner is
// recording, so the fixture written next is the WHOLE evidence the monitor
// reads. The runner keeps writing to its now-orphaned inode; the analyser
// reads the path, which is the fixture's.
func onlyFixtureTranscript(t *testing.T, harp string) {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
}

// stuckChildTranscript reproduces the stuck-loop failure mode for harp: a
// relaunch per delivery (a fresh transcript.Recorder, hence seq restarting at
// 0 against the same O_APPEND file), the same composed context every time, no
// turn ever.
func stuckChildTranscript(t *testing.T, harp string, deliveries int) {
	t.Helper()
	onlyFixtureTranscript(t, harp)
	for i := 0; i < deliveries; i++ {
		rec, err := transcript.NewRecorder(harp, "claude")
		require.NoError(t, err)
		transcript.RecordUserText(rec, "# composed context\n\nyou are a delegated agent\n")
		require.NoError(t, rec.Close())
	}
}

func healthyChildTranscript(t *testing.T, harp string) {
	t.Helper()
	onlyFixtureTranscript(t, harp)
	rec, err := transcript.NewRecorder(harp, "claude")
	require.NoError(t, err)
	defer func() { require.NoError(t, rec.Close()) }()
	transcript.RecordUserText(rec, "# composed context\n\nyou are a delegated agent\n")
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "on it",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolUse, ToolName: "Read",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}))
}

func reportFor(reps []liveness.Report, harp string) *liveness.Report {
	for i := range reps {
		if reps[i].Harp == harp {
			return &reps[i]
		}
	}
	return nil
}

// spawnOneChild launches a single fake child and returns its harp.
func spawnOneChild(t *testing.T, c *Coordinator) string {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(context.Background(), out.Harp))
	return out.Harp
}

func TestLivenessSnapshot_FiresOnStuckChildAndNotOnHealthyOne(t *testing.T) {
	livenessTestHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	stuckHarp := spawnOneChild(t, c)
	healthyHarp := spawnOneChild(t, c)
	stuckChildTranscript(t, stuckHarp, 6)
	healthyChildTranscript(t, healthyHarp)

	reps := c.livenessSnapshot(context.Background())
	require.NotEmpty(t, reps, "the snapshot must cover the children the coordinator holds")

	stuck := reportFor(reps, stuckHarp)
	require.NotNil(t, stuck, "the stuck child never reached the monitor at all")
	assert.Equal(t, liveness.StateStalled, stuck.State,
		"the coordinator must be able to tell looping from working: %s", stuck.Reason)
	assert.True(t, stuck.Firing())
	assert.True(t, stuck.Evidence.Transcript.SeqPinned)
	assert.Zero(t, stuck.Evidence.Transcript.AssistantEntries)

	healthy := reportFor(reps, healthyHarp)
	require.NotNil(t, healthy)
	assert.False(t, healthy.Firing(), "a working child must never be reported as stalled: %s", healthy.Reason)
}

// A PARKED child must suppress the verdict even when the transcript looks
// exactly like the stuck one — a park can hold a child for minutes (waiting
// in agent_recv, or waiting on a permission decision at its engine), and
// reaping one turns a working system into one that kills its own children.
//
// The briefing turn is held OPEN for the whole test. That is not a
// convenience: a parked child is one blocked INSIDE its agent_recv tool call
// (AgentRecv calls onRolePark and then blocks on the poll), so its turn
// boundary cannot land until it unparks. A fake whose turn auto-completes
// races that boundary's StateIdle against the park — a sequence production
// cannot produce — and whichever journals last decides the verdict.
func TestLivenessSnapshot_ParkSuppressesTheVerdict(t *testing.T) {
	livenessTestHome(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	harp := spawnOneChild(t, c)
	stuckChildTranscript(t, harp, 6)

	// Unparked, this child fires.
	before := reportFor(c.livenessSnapshot(context.Background()), harp)
	require.NotNil(t, before)
	require.Equal(t, liveness.StateStalled, before.State, "precondition: %s", before.Reason)

	// Park it through the production path: a child waiting in agent_recv
	// yields its slot mid-turn.
	c.onRolePark(harp)
	c.mu.Lock()
	rt := c.byHarp[harp]
	c.mu.Unlock()
	require.NotNil(t, rt, "precondition: the spawned child must have a runtime attachment")
	require.Equal(t, StateParked, c.runState(rt.runID), "precondition: onRolePark must have journaled the park")

	after := reportFor(c.livenessSnapshot(context.Background()), harp)
	require.NotNil(t, after)
	assert.Equal(t, liveness.StateAwaitingApproval, after.State,
		"a PARKED child must NEVER be reported stalled: %s", after.Reason)
	assert.False(t, after.Firing())

	close(gate) // release the held turn so the child's turnGate goroutine doesn't leak past the test
}

// The transcript path the adapter hands the monitor must be the one the
// recorder actually writes — an adapter pointing at nothing would report every
// child as "no transcript" and be indistinguishable from a working monitor
// watching genuinely silent agents.
func TestLivenessTargets_ResolveTheCanonicalTranscriptPath(t *testing.T) {
	livenessTestHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	harp := spawnOneChild(t, c)

	targets := c.livenessTargets()
	require.Len(t, targets, 1)
	want, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	assert.Equal(t, want, targets[0].TranscriptPath)
	assert.Equal(t, harp, targets[0].Harp)
	assert.NotEmpty(t, targets[0].Runtime, "every target must carry a runtime axis so a probe can claim it")
	assert.False(t, targets[0].StartedAt.IsZero(), "without a start time no age-gated rule can ever apply")
}

// A connected runner beating recently is positive evidence of life.
func TestRunnerHeartbeatProbe_LiveRunnerIsAlive(t *testing.T) {
	livenessTestHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	harp := spawnOneChild(t, c)

	var credHash string
	c.runs.View(func() { credHash = c.runsF.currentRun(harp).CredHash })
	require.NotEmpty(t, credHash)
	c.mu.Lock()
	c.runners[credHash] = newRunnerSession(credHash, "run-x", c.now(), func() {})
	c.mu.Unlock()

	st := c.runnerHeartbeatProbe().Inspect(context.Background(), liveness.Target{Harp: harp})
	assert.True(t, st.Observed)
	assert.True(t, st.Alive)

	// Past the loss bound the same probe reports dead — the SAME bound
	// runnerWatchdog uses, so the two can never disagree.
	c.mu.Lock()
	c.runners[credHash].lastBeat = c.now().Add(-runnerLossTimeout - time.Second)
	c.mu.Unlock()
	st = c.runnerHeartbeatProbe().Inspect(context.Background(), liveness.Target{Harp: harp})
	assert.True(t, st.Observed)
	assert.False(t, st.Alive)
}
