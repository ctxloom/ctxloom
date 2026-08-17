package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
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

// stuckChildTranscript reproduces the stuck-loop failure mode for harp: a
// relaunch per delivery (a fresh transcript.Recorder, hence seq restarting at
// 0 against the same O_APPEND file), the same composed context every time, no
// turn ever.
func stuckChildTranscript(t *testing.T, harp string, deliveries int) {
	t.Helper()
	for i := 0; i < deliveries; i++ {
		rec, err := transcript.NewRecorder(harp, "claude")
		require.NoError(t, err)
		transcript.RecordUserText(rec, "# composed context\n\nyou are a delegated agent\n")
		require.NoError(t, rec.Close())
	}
}

func healthyChildTranscript(t *testing.T, harp string) {
	t.Helper()
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

// An outstanding approval must suppress the verdict even when the transcript
// looks exactly like the stuck one — an approval rung can hold a child for
// minutes, and reaping one turns a working system into one that kills its own
// children.
func TestLivenessSnapshot_ApprovalParkSuppressesTheVerdict(t *testing.T) {
	livenessTestHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	harp := spawnOneChild(t, c)
	stuckChildTranscript(t, harp, 6)

	// Without the approval, this child fires.
	before := reportFor(c.livenessSnapshot(context.Background()), harp)
	require.NotNil(t, before)
	require.Equal(t, liveness.StateStalled, before.State, "precondition: %s", before.Reason)

	// Register an outstanding approval addressed to it, exactly as
	// relayApproval does (including its lazy map init).
	c.mu.Lock()
	if c.approvals == nil {
		c.approvals = make(map[string]*pendingApproval)
	}
	c.approvals["msg-1"] = &pendingApproval{targetHarp: harp}
	c.mu.Unlock()

	after := reportFor(c.livenessSnapshot(context.Background()), harp)
	require.NotNil(t, after)
	assert.Equal(t, liveness.StateAwaitingApproval, after.State,
		"a child parked on an approval must NEVER be reported stalled: %s", after.Reason)
	assert.False(t, after.Firing())
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

// The heartbeat probe must report Observed:false — never "dead" — for a run
// with no connected runner, or the legacy chat path (which never dials home)
// would be declared dead on sight.
func TestRunnerHeartbeatProbe_AbsentRunnerIsUnobservedNotDead(t *testing.T) {
	livenessTestHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	harp := spawnOneChild(t, c)

	st := c.runnerHeartbeatProbe().Inspect(context.Background(), liveness.Target{Harp: harp})
	assert.False(t, st.Observed, "no connected runner means we know nothing, not that it died")
	assert.False(t, st.Alive)
	assert.NotEmpty(t, st.Detail, "an unobserved target must say why")
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
