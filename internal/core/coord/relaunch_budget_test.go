package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The launch-retry budget bounded LAUNCH FAILURES only.
//
// `fails` is incremented by noteLaunchFailure and reset to zero by
// noteLaunchAttached — so a child that ATTACHES and then dies without ever
// consuming its mail resets the budget on every cycle. terminateRun's tail
// sees pendingCount > 0, nextRelaunch answers (backoff(0)=0, true), and the
// loop closes with no counter and no delay: the same unbounded-relaunch hazard
// (846 zombies, ~2 container launches/sec for 49 minutes, observed once) through
// a different door, and precisely the observed `runtime:container` "starts,
// gets nothing, exits" behaviour.
//
// The bound that actually holds is on RELAUNCH ATTEMPTS, reset only when the
// harp demonstrably CONSUMED mail — attaching is not progress, draining is.

// simulateAttachThenDie runs the attach-then-die-with-mail-pending cycle up to
// `cap` times, returning how many relaunches the gate authorised and whether
// any of them was made to wait. A gate that bounds this loop refuses well
// before cap.
func simulateAttachThenDie(c *Coordinator, harp string, cap int) (authorised int, backedOff bool) {
	for range cap {
		// The launch came up: StartRun round-tripped, markAttached ran.
		c.noteLaunchAttached(harp)
		// ...and the child then died without draining its mailbox, so
		// terminateRun's tail asks whether it may bring the harp back.
		d, ok, _ := c.nextRelaunch(harp)
		if !ok {
			return authorised, backedOff
		}
		authorised++
		if d > 0 {
			backedOff = true
		}
	}
	return authorised, backedOff
}

// TestRelaunchBudget_BoundsAttachThenDie is the load-bearing assertion: an
// attach is NOT progress. A harp that comes up and dies again without
// consuming mail must exhaust the budget just like one that never came up.
func TestRelaunchBudget_BoundsAttachThenDie(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {}}, nil)
	c := newTestCoordinator(t, sp, nil)

	authorised, backedOff := simulateAttachThenDie(c, "spin-harp", 200)

	assert.LessOrEqual(t, authorised, c.maxLaunchAttempts,
		"the relaunch loop is unbounded for attach-then-die: the gate authorised %d relaunches "+
			"because a mere attach resets the budget", authorised)
	assert.True(t, backedOff,
		"every authorised relaunch ran at zero backoff — a hot spin, not a retry")
}

// TestRelaunchBudget_ResetByMailConsumption pins the other half of the
// discriminator: the budget resets on real progress. A harp that actually
// drains a message earns a fresh budget, so a long-lived child that takes one
// turn per relaunch is never throttled by this bound.
func TestRelaunchBudget_ResetByMailConsumption(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {}}, nil)
	c := newTestCoordinator(t, sp, nil)

	const harp = "progress-harp"
	first, _ := simulateAttachThenDie(c, harp, 200)
	if !assert.Positive(t, first, "precondition: the gate must authorise at least one relaunch") {
		return
	}
	if _, ok, _ := c.nextRelaunch(harp); !assert.False(t, ok, "precondition: the budget must be exhausted") {
		return
	}

	c.noteMailConsumed(harp) // the child drained a message: real progress

	_, ok, _ := c.nextRelaunch(harp)
	assert.True(t, ok,
		"consuming mail did not clear the relaunch budget: a healthy child that takes one turn "+
			"per run would be permanently refused after %d turns", first)
}

// TestRelaunchBudget_ResetByExplicitDelivery keeps clearLaunchGate's
// documented contract whole — an operator or parent asking again is a fresh
// start for BOTH counters, not just the launch-failure one.
func TestRelaunchBudget_ResetByExplicitDelivery(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {}}, nil)
	c := newTestCoordinator(t, sp, nil)

	const harp = "asked-again-harp"
	simulateAttachThenDie(c, harp, 200)
	if _, ok, _ := c.nextRelaunch(harp); !assert.False(t, ok, "precondition: the budget must be exhausted") {
		return
	}

	c.clearLaunchGate(harp)

	_, ok, _ := c.nextRelaunch(harp)
	assert.True(t, ok, "an explicit new delivery must lift the relaunch bound, as it lifts the launch-failure one")
}
