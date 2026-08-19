package coord

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Acceptance (2): a docker-stop kills the runner AND the harness together, so
// RunExited is never sent — the coordinator must detect RUNNER LOSS
// (disconnect ∪ missed heartbeats) and synthesize the terminal within a
// stated bound, advancing the queue and landing a terminal notice in the
// parent's mailbox. These are deterministic tests with a controllable clock
// and a live RunnerChannel.

// dialFakeRunner opens a RunnerChannel to the coordinator's real gRPC endpoint
// using the credential the coordinator minted for runID.
func dialFakeRunner(t *testing.T, c *Coordinator, runID string) *RunnerLink {
	t.Helper()
	// The child's credential is the one enqueueRun minted; the test reaches
	// it via the run record's cred hash → we can't recover the token from the
	// hash, so instead we mint a runner link straight from the env the child
	// engine would have received. The conformance path already asserts the
	// engine gets EnvCoordCred; here we reuse that exact token.
	env := waitForChildEnv(t, c, runID)
	link, err := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", nil)
	require.NoError(t, err)
	return link
}

// waitForChildEnv resolves the env a launched child received (its credential
// rides only there). It polls the fake spawner's engine list.
func waitForChildEnv(t *testing.T, c *Coordinator, runID string) map[string]string {
	t.Helper()
	sp := c.spawner.(*fakeSpawner)
	deadline := time.Now().Add(conformanceWait)
	for time.Now().Before(deadline) {
		sp.mu.Lock()
		for _, e := range sp.engines {
			env := e.runnerEnv()
			if env[EnvRunID] == runID {
				sp.mu.Unlock()
				return env
			}
		}
		sp.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child env for run %s never appeared", runID)
	return nil
}

// TestRunnerLoss_DisconnectSynthesizesExit pins the docker-stop path: closing
// the RunnerChannel is runner loss, so the coordinator synthesizes the
// terminal for the runner's active runs, the roster shows ended, the queue
// advances, and the parent's mailbox gets the synthesized exit notice.
func TestRunnerLoss_DisconnectSynthesizesExit(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinatorCap(t, sp, nil, 1) // pin cap=1: this test exercises D4 QUEUEING past the cap, not the (now-configurable) default cap value

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	// A second run queues behind the cap.
	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task two", "", "")
	require.NoError(t, err)
	require.True(t, second.Queued)

	// The container's runner dials home, then the container is docker-stopped:
	// the RunnerChannel disconnects (Shutdown without the harness having sent a
	// terminal). That disconnect is runner loss.
	link := dialFakeRunner(t, c, first.RunID)
	// Close the harness-side turn too so the fake engine doesn't also emit a
	// chat-close terminal that races (the exactly-once seam handles either,
	// but we want to observe the RUNNER-LOSS cause deterministically).
	link.cancel()

	require.Eventually(t, func() bool { return rosterState(c, first.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"a RunnerChannel disconnect must synthesize the terminal")

	// The queue advanced: the second child started once the slot freed.
	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"runner loss frees the slot and the queue advances")

	// The parent learned: a synthesized exit notice is in its mailbox.
	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	found := false
	for _, m := range msgs {
		if m.Kind == KindExited && m.From == first.Harp {
			found = true
		}
	}
	assert.True(t, found, "the parent's mailbox gets the synthesized exit notice (blue-paper fix)")
}

// TestRunnerLoss_HeartbeatTimeout pins the silent-death path: a runner that
// stops heartbeating (no disconnect) is declared lost by the watchdog after
// the loss bound. Driven with an explicit clock so the bound is deterministic.
func TestRunnerLoss_HeartbeatTimeout(t *testing.T) {
	resetStrictness(t)
	now := time.Unix(1_700_000_000, 0)
	var clockMu chanClock
	clockMu.set(now)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, clockMu.now)

	run, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	link := dialFakeRunner(t, c, run.RunID)
	// The runner is registered; simulate a live heartbeat at `now`.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.runners) == 1
	}, conformanceWait, 10*time.Millisecond)

	// Advance the clock past the loss bound and run the watchdog body directly
	// (deterministic — no ticker timing).
	c.checkRunnerLiveness(now.Add(runnerLossTimeout + time.Second))

	require.Eventually(t, func() bool { return rosterState(c, run.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"a runner silent past the loss bound is declared lost")
	// Keep the link referenced so its goroutines don't get GC'd mid-test.
	link.cancel()
}

// chanClock is a tiny mutable clock for deterministic watchdog tests.
type chanClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *chanClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *chanClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
