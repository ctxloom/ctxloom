package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startRunAbortBudget is how long this test is willing to wait for the
// cancelled round trip to unwind. It sits far below defaultRequestTimeout
// (60s), which is exactly what discriminates: a StartRun bound to c.baseCtx
// instead of the per-harp launch context can only ever end on that clock,
// so an agent_stop is invisible to it.
const startRunAbortBudget = 15 * time.Second

// TestIssueStartRun_CancelAbortsTheRoundTrip pins the reach of agent_stop
// past the dial-home barrier. issueStartRun takes the cancellable per-harp
// launch context (launchgate.go's, the one AgentStop cancels) and used it
// only for the awaitRunner wait, then built the StartRun round trip from
// c.baseCtx — which nothing but coordinator shutdown ever cancels. A stop
// issued after the runner dialed home but before it answered StartRun
// therefore could not abort anything: the coordinator stayed parked on the
// answer for the full request budget.
//
// The runner here is registered but deliberately mute: the StartRun frame
// reaches its send queue and no RunnerResponse ever comes back.
func TestIssueStartRun_CancelAbortsTheRoundTrip(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", viaStartRun: true}}, nil)
	c := newTestCoordinator(t, sp, nil)

	plan, err := sp.Resolve(context.Background(), "worker")
	require.NoError(t, err)
	rt, token, err := c.enqueueRun(ownerIdentity(), plan, "child-stop-harp", "brief", false, make(chan struct{}), 1)
	require.NoError(t, err)
	credHash := hashToken(token)

	// A connected-but-mute runner: awaitRunner resolves instantly, and the
	// StartRun request parks on an answer that never arrives.
	rs := newRunnerSession(credHash, rt.runID, time.Now(), func() {})
	c.mu.Lock()
	c.runners[credHash] = rs
	c.mu.Unlock()

	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness: plan.Backend, Model: "test-model", Workspace: t.TempDir(),
		SessionHarp: rt.harp, Permission: plan.Perm,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.issueStartRun(ctx, rt, credHash, spec, "the first turn", "test-model", "") }()

	// Past the dial-home wait: the StartRun frame is on the runner's send
	// queue, so the coordinator is parked on the RESPONSE — which is the
	// exact window agent_stop could not reach.
	select {
	case frame := <-rs.send:
		require.NotNil(t, frame.GetRequest().GetStartRun(), "the frame on the wire must be the StartRun")
		assert.Equal(t, rt.runID, frame.GetRequest().GetStartRun().GetRunId())
	case <-time.After(startRunAbortBudget):
		t.Fatal("StartRun was never issued, so this test never reached the window it is about")
	}

	cancel() // agent_stop cancels the per-harp launch context
	select {
	case err := <-done:
		require.Error(t, err, "a cancelled StartRun must report the abort, never succeed")
		assert.ErrorIs(t, err, context.Canceled,
			"the abort must be attributed to the cancellation, got %v", err)
	case <-time.After(startRunAbortBudget):
		t.Fatalf("cancelling the per-harp launch context did not abort the StartRun round trip within %s", startRunAbortBudget)
	}
}
