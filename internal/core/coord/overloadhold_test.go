package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestOverloadHold_BacksOffThatRunAlone: a turn turned away because the
// engine's server is at capacity parks THAT run for the overload backoff —
// the runs sharing its credential carry on, since overload is not the
// credential's limit. An agent may not cut the wait short; at its end the
// hold releases itself and the mail held meanwhile runs.
func TestOverloadHold_BacksOffThatRunAlone(t *testing.T) {
	f, clk := newRateFixture(t)

	f.send(t, f.worker, overloadHit+" do the work")
	hold := f.awaitHold(t, f.worker)
	assert.Equal(t, agent.FailureOverloaded, hold.Kind)
	assert.True(t, clk.Now().Add(overloadBackoff).Equal(hold.Until), "got %v", hold.Until)
	f.awaitParks(t, f.worker)
	assertOpened(t, f.c, agent.FailureOverloaded, holdScopeRun)
	parked := readFacts[holdParked](t, f.c, factHoldParked)
	require.Len(t, parked, 1)
	assert.Equal(t, "turn", parked[0].Cause)
	assert.Equal(t, 1, f.findingsWith("overloaded"), "ONE finding for the backoff: %v", f.findings.All())
	assert.Zero(t, f.findingsWith("rate limit"), "an overload is not reported as a rate limit")

	held := f.entry(f.worker).Hold
	require.NotNil(t, held)
	assert.Equal(t, string(agent.FailureOverloaded), held.Kind)
	assert.Equal(t, hold.Source.Key, held.Source, "the roster's source is the run's credential carriers, never the hold's own key")
	assert.Equal(t, held, f.holdOf(t, f.worker), "one hold, both rosters")
	assert.Nil(t, f.entry(f.sibling).Hold, "the credential's other run is not held")

	f.send(t, f.worker, "held work")
	f.send(t, f.sibling, "free work")
	awaitChatText(t, f.sp, 1, "free work")

	_, err := f.c.ControlResume(human(t), ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}, f.worker)
	require.ErrorIs(t, err, ErrCredentialHeld, "an agent may not cut the backoff short")

	clk.Advance(overloadBackoff - time.Second)
	assert.Len(t, f.c.CredentialHolds(), 1, "a second early, the backoff stands")
	assert.Zero(t, countChatText(f.sp, 0, "held work"), "a backed-off run takes no new turn")

	clk.Advance(time.Second) // the release runs on this goroutine, inside Advance
	assert.Empty(t, f.c.CredentialHolds(), "at the backoff's end the hold releases itself")
	assertReleased(t, f.c, "backoff")
	awaitChatText(t, f.sp, 0, "held work")
}

// TestOverloadHold_ARunAlreadyHeldStaysInItsHold forces the interleaving: the
// sibling's turn is inside its engine when its credential's rate-limit hold
// parks it, and that turn then ends overloaded. The run is already held, so
// the overload opens no second hold and does not re-key the run — the one
// rate-limit hold still releases it.
func TestOverloadHold_ARunAlreadyHeldStaysInItsHold(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(1)
	f.send(t, f.sibling, overloadHit+" slow work")
	awaitChatText(t, f.sp, 1, "slow work") // the sibling's turn is inside its engine
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling) // the park reached the sibling mid-turn
	release()
	f.awaitFolds(t, 2)

	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.Equal(t, agent.FailureRateLimited, hold.Kind, "the overload does not relabel the credential's hold")
	assert.Zero(t, f.findingsWith("overloaded"), "no backoff of its own: %v", f.findings.All())

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	f.send(t, f.sibling, "after the race")
	awaitChatText(t, f.sp, 1, "after the race")
}

// An overload is the server's capacity, not the credential's: its hold is the
// run's own even when the run shares a credential.
func TestHoldKey_AnOverloadHoldsTheRunAlone(t *testing.T) {
	src := engine.Credentials{Env: map[string]string{"X": "v"}}.Source("e")
	assert.Equal(t, holdScopeRun, holdScopeOf(agent.FailureOverloaded, src))
	assert.Equal(t, holdScopeCredential, holdScopeOf(agent.FailureRateLimited, src))
	assert.Equal(t, holdScopeRun, holdScopeOf(agent.FailureRateLimited, engine.CredentialSource{}), "a run with no credential holds alone")
	assert.NotEqual(t, holdKey(agent.FailureOverloaded, src, "run-a"), holdKey(agent.FailureOverloaded, src, "run-b"))
	assert.Equal(t, holdKey(agent.FailureRateLimited, src, "run-a"), holdKey(agent.FailureRateLimited, src, "run-b"))
}

// The overload backoff ignores any reset time: a 529 names none, and the
// rate-limit clamp is not its policy.
func TestHoldDeadline_OverloadWaitsItsOwnBackoff(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	assert.True(t, now.Add(overloadBackoff).Equal(holdDeadline(now, agent.TurnFailure{Kind: agent.FailureOverloaded})))
	resets := now.Add(10 * time.Minute)
	assert.True(t, resets.Equal(holdDeadline(now, agent.TurnFailure{Kind: agent.FailureRateLimited, ResetsAt: resets})))
}
