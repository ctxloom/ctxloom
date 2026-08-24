package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// childRt.slotHeld used to conflate "holds a slot" with "intends
// to acquire one". claimSlotIntent set the (single) bit true BEFORE a
// potentially long BLOCKING c.slots.Acquire (onRoleUnpark's shape); a
// concurrent releaseSlot/onRolePark landing inside that window could not
// tell "claiming" from "holding" apart:
//   - releasing against a merely-claimed bit hands back a token nobody has
//     actually taken from the pool yet — which the semaphore answers with a
//     panic rather than an inflated cap (see the over-release pin); and
//   - clearing the bit under a claim that goes on to land a real slot
//     anyway leaves that slot's bit never set again — LEAKING it forever
//     (free permanently short of the cap).
//
// This test reproduces the exact interleaving directly against the
// claimSlotIntent/commitSlotClaim/releaseSlot primitives (rather than through
// the full onRoleUnpark/mailbox/RunChannel machinery, which needs a live
// run+journal to reach StateParked) — the review that filed this finding
// noted the existing turncap_concurrent invariant test sets TurnCap == the
// child count, so c.slots.Acquire never actually blocks and this window
// is never exercised; TurnCap here is 1 specifically to force it to.
func TestSlotClaim_ReleaseDuringBlockingAcquire_DoesNotLeakOrInflate(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinatorCap(t, sp, nil, 1) // exactly one slot total

	rt := &childRt{harp: "child-a", runID: "run-a"}

	// A different occupant holds the only slot, so rt's claim below must
	// genuinely BLOCK on c.slots.Acquire, not resolve immediately.
	require.True(t, c.slots.TryAcquire(1), "precondition: the one slot is free to occupy")

	require.True(t, c.claimSlotIntent(rt), "the claim itself must succeed even though no slot is free yet")

	acquireDone := make(chan struct{})
	go func() {
		defer close(acquireDone)
		err := c.slots.Acquire(context.Background(), 1)
		if !assert.NoError(t, err, "a slot IS coming (the occupant releases below)") {
			return
		}
		if keep := c.commitSlotClaim(rt); !keep {
			// Cancelled while the acquire was in flight: this landed slot
			// was never wanted — give it straight back.
			c.slots.Release(1)
		}
	}()

	// Let the goroutine actually reach the blocking acquire before racing it.
	time.Sleep(30 * time.Millisecond)

	// THE RACE: something (onRolePark/onTurnIdle's releaseSlot) decides rt
	// no longer needs a slot WHILE rt is still only slotClaimed.
	c.releaseSlot(rt)

	// Now the original occupant gives its real slot back, letting the
	// blocked acquire above land.
	c.slots.Release(1)

	select {
	case <-acquireDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the blocked acquire never completed — the test's own setup is broken")
	}

	assert.True(t, slotsIdleWith(c.slots, 1),
		"exactly one slot (the cap) must be free at the end — not 0 (the late-arriving real slot LEAKED "+
			"because nothing ever committed or released it). The phantom-release half of this defect can no "+
			"longer reach a wrong count at all: a release against a merely-claimed slot panics in Release "+
			"before it can inflate anything")

	c.mu.Lock()
	finalState := rt.slot
	c.mu.Unlock()
	assert.Equal(t, slotFree, finalState, "rt must end up owning no slot: it was released before its acquire landed")
}
