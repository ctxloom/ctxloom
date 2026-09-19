package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An over-release of the execution-slot cap must be LOUD.
//
// slotState's doc names two defects the claim/commit dance exists to prevent,
// and this is the first of them: releasing against a slot nobody has actually
// taken from the pool. The hand-rolled slot queue this cap used to be answered
// that with `free++`, which raised the ceiling above its CONFIGURED value and
// then said nothing — the coordinator would admit a fifth live engine process
// under a cap of four, and every subsequent over-release would raise it again,
// with no log line, no error, and no test able to see it from the outside. The
// symptom surfaces as memory exhaustion on a box, hours away from the cause.
//
// semaphore.Weighted panics instead, and the panic is the wanted behaviour:
// the cap is a resource ceiling on live engine processes, so a coordinator
// that has lost count of them is not a coordinator worth continuing. Nothing
// here recovers, guards, or downgrades it to a log — the assertions below pin
// the crash itself as the contract.
func TestReleaseSlot_OverReleaseIsLoudNotSilentCapInflation(t *testing.T) {
	// A childRt whose bookkeeping says slotHeld while no token was ever taken
	// is exactly the state a mis-ordered release produces. releaseSlot acts on
	// that bit, so it hands back a token the semaphore never issued.
	t.Run("releasing a slot that was never acquired panics", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinatorCap(t, sp, nil, 1)
		rt := &childRt{harp: "child-a", runID: "run-a", slot: slotHeld}

		require.True(t, slotsIdleWith(c.slots, 1), "precondition: the cap's one token is still in the pool")

		assert.PanicsWithValue(t, "semaphore: released more than held", func() { c.releaseSlot(rt) },
			"a release against a slot nobody holds must CRASH, not raise the configured cap by one in silence")
	})

	// The other half of the contract: loudness must not fire on the states
	// releaseSlot is supposed to handle quietly. A merely-CLAIMED rt has an
	// acquisition still in flight, so its release is deferred to
	// commitSlotClaim rather than performed here — and a slotFree rt holds
	// nothing to give back at all.
	t.Run("the guarded states release nothing and stay quiet", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinatorCap(t, sp, nil, 1)

		claimed := &childRt{harp: "child-b", runID: "run-b", slot: slotClaimed}
		free := &childRt{harp: "child-c", runID: "run-c", slot: slotFree}

		assert.NotPanics(t, func() { c.releaseSlot(claimed) },
			"a claim whose acquisition is still in flight is cancelled, not released — releasing it here is the over-release the panic above catches")
		assert.NotPanics(t, func() { c.releaseSlot(free) },
			"an rt that holds nothing has nothing to give back")

		assert.True(t, slotsIdleWith(c.slots, 1),
			"neither guarded release may put a token back: the pool must still hold exactly the cap, never more")
		c.mu.Lock()
		defer c.mu.Unlock()
		assert.True(t, claimed.slotCancel, "the cancelled claim must be recorded so commitSlotClaim gives the landed slot back")
	})
}
