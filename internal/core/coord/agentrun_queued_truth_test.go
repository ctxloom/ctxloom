package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queuedTruthWait bounds the seam's park. It is a barrier budget, not an
// assertion budget: what is being waited for is the child goroutine reaching
// its terminal, and the test's verdict does not depend on how long that took.
const queuedTruthWait = 20 * time.Second

// TestAgentRun_QueuedDispositionIsTruthful pins RunOutcome.Queued to the fact
// it names. Queued means "this child is waiting behind the execution cap" —
// a state a caller waits out rather than investigates. It was computed from
// rt.slot AFTER the child's driver goroutine had already been dispatched, and
// that goroutine OWNS rt.slot: it promotes the enqueue's claim, and its
// terminal releases the slot back to slotFree. So a child that was admitted
// with a slot in hand and then died at standup answered "queued behind the
// execution cap" — a true statement about some other run.
//
// The spawn here cannot possibly have been queued: the concurrency cap is
// wide open, the enqueue claimed a slot, and the engine launch was actually
// attempted. It merely FAILED, which is a different disposition entirely.
func TestAgentRun_QueuedDispositionIsTruthful(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.launchErr = errors.New("engine binary is missing")
	c := newTestCoordinator(t, sp, nil)

	// Park agent_run right after the dispatch until the child has actually
	// given its slot back, so "the goroutine got there first" is a fact of
	// this test rather than a scheduler coin flip.
	c.spawnDispatchedHook = func(harp string) {
		deadline := time.Now().Add(queuedTruthWait)
		for time.Now().Before(deadline) {
			c.mu.Lock()
			rt := c.byHarp[harp]
			done := rt != nil && rt.slot != slotHeld
			c.mu.Unlock()
			if done {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Errorf("the dispatched child never released its slot within %s, so this test never reached the window it is about", queuedTruthWait)
	}

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// Corroboration, independent of the disposition itself: the engine
	// launch was ATTEMPTED, so this child never waited behind the cap.
	require.Equal(t, 1, sp.spawnCount(), "the child's engine launch must have been attempted (a queued child never reaches Launch)")
	assert.False(t, out.Queued,
		"a child that was admitted immediately and then failed to launch is not queued behind the execution cap")
}
