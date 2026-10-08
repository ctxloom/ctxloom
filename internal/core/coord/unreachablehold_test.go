package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestUnreachableHold_BacksOffThatRunAlone: a turn turned away because the
// engine could not reach its model endpoint parks THAT run for the endpoint
// backoff, like an overload: the credential is not at fault, so the runs
// sharing it carry on. At the backoff's end the hold releases itself and the
// mail held meanwhile runs.
func TestUnreachableHold_BacksOffThatRunAlone(t *testing.T) {
	f, clk := newRateFixture(t)

	f.send(t, f.worker, unreachableHit+" do the work")
	hold := f.awaitHold(t, f.worker)
	assert.Equal(t, agent.FailureEndpointUnreachable, hold.Kind)
	assert.True(t, clk.Now().Add(overloadBackoff).Equal(hold.Until), "got %v", hold.Until)
	f.awaitParks(t, f.worker)
	assertOpened(t, f.c, agent.FailureEndpointUnreachable, holdScopeRun)
	f.awaitFolds(t, 1)
	assert.Equal(t, 1, f.findingsWith("could not reach its model endpoint"), "ONE finding for the backoff: %v", f.findings.All())
	assert.Zero(t, f.findingsWith("rate limit"), "an unreachable endpoint is not reported as a rate limit")
	assert.Zero(t, f.findingsWith("overloaded"), "nor as an overload")

	held := f.entry(f.worker).Hold
	require.NotNil(t, held)
	assert.Equal(t, string(agent.FailureEndpointUnreachable), held.Kind)
	assert.Nil(t, f.entry(f.sibling).Hold, "the credential's other run is not held")

	f.send(t, f.worker, "held work")
	f.send(t, f.sibling, "free work")
	awaitChatText(t, f.sp, 1, "free work")

	clk.Advance(overloadBackoff - time.Second)
	assert.Len(t, f.c.CredentialHolds(), 1, "a second early, the backoff stands")
	assert.Zero(t, countChatText(f.sp, 0, "held work"), "a backed-off run takes no new turn")

	clk.Advance(time.Second)
	assert.Empty(t, f.c.CredentialHolds(), "at the backoff's end the hold releases itself")
	assertReleased(t, f.c, "backoff")
	awaitChatText(t, f.sp, 0, "held work")
}

// An unreachable endpoint is held (the runner parks on it), and is an
// endpoint failure: its hold is the run's own, keyed per run, and waits the
// fixed backoff whatever reset time a failure might carry.
func TestEndpointUnreachable_IsAHeldRunScopedFailure(t *testing.T) {
	assert.True(t, HoldsFailure(agent.FailureEndpointUnreachable))
	assert.True(t, RunScopedFailure(agent.FailureEndpointUnreachable))
	assert.True(t, RunScopedFailure(agent.FailureOverloaded))
	assert.False(t, RunScopedFailure(agent.FailureRateLimited))
	assert.False(t, RunScopedFailure(agent.FailureCredentialRejected))

	src := engine.Credentials{Env: map[string]string{"X": "v"}}.Source("e")
	assert.Equal(t, holdScopeRun, holdScopeOf(agent.FailureEndpointUnreachable, src))
	assert.NotEqual(t, holdKey(agent.FailureEndpointUnreachable, src, "run-a"), holdKey(agent.FailureEndpointUnreachable, src, "run-b"))

	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	got := holdDeadline(now, agent.TurnFailure{Kind: agent.FailureEndpointUnreachable, ResetsAt: now.Add(time.Hour)})
	assert.True(t, now.Add(overloadBackoff).Equal(got), "got %v", got)

	k, ok := failureKindOf(string(agent.FailureEndpointUnreachable))
	assert.True(t, ok, "a journaled endpoint hold is read back as its failure after a restart")
	assert.Equal(t, agent.FailureEndpointUnreachable, k)
}
