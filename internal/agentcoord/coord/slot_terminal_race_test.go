package coord

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// slotsIdleWith reports whether the execution-slot semaphore has EXACTLY
// want tokens free and nobody parked on it, without disturbing either.
// semaphore.Weighted publishes no counters, but TryAcquire is a total
// probe of both facts: it refuses to barge past an enrolled waiter, so
// TryAcquire(want) succeeds only when the waiter queue is empty AND at
// least want tokens are free, and the follow-up TryAcquire(1) must then
// fail for want to be the exact count rather than a lower bound. Whatever
// the probe takes it gives straight back.
//
// TryAcquire(0) is the degenerate case and the useful one: it takes no
// tokens at all, so it succeeds precisely when nothing is parked — a
// read-only look at the waiter queue.
func slotsIdleWith(s *semaphore.Weighted, want int64) bool {
	if !s.TryAcquire(want) {
		return false
	}
	exact := !s.TryAcquire(1)
	if !exact {
		s.Release(1)
	}
	s.Release(want)
	return exact
}

// waitForSlotWaiter blocks until a goroutine is parked in Acquire on the
// execution-slot semaphore. This is the test's SYNCHRONISATION POINT, not a
// hope: a waiter is enrolled inside Acquire and nowhere else, so observing
// the enrolment proves the acquirer is genuinely parked on the cap — and,
// because the claim precedes the acquire in acquireRunSlot, that its claim
// (if the guard exists at all) is already recorded. Nothing below races a
// sleep.
func waitForSlotWaiter(t *testing.T, s *semaphore.Weighted) {
	t.Helper()
	require.Eventually(t, func() bool { return !s.TryAcquire(0) }, conformanceWait, time.Millisecond,
		"an acquirer must actually park on the execution-slot cap before the terminal races it")
}

// blockingLaunchSpawner records the first legacy Launch and then HOLDS it, so
// a run that reaches Launch cannot quietly give its execution slot back at its
// own stream end. Without the hold the cap-shrink defect is INVISIBLE: the
// terminated run goes on to launch, drive a fake engine to completion, and
// release the slot at endChild — the pool recovers by accident and the
// assertion passes with the bug still in place.
type blockingLaunchSpawner struct {
	*fakeSpawner
	once     sync.Once
	launched chan struct{}
	hold     chan struct{}
}

func newBlockingLaunchSpawner(t *testing.T, agents map[string]fakeAgent) *blockingLaunchSpawner {
	t.Helper()
	s := &blockingLaunchSpawner{
		fakeSpawner: newFakeSpawner(agents, nil),
		launched:    make(chan struct{}),
		hold:        make(chan struct{}),
	}
	t.Cleanup(func() { close(s.hold) })
	return s
}

func (s *blockingLaunchSpawner) Launch(_ context.Context, _ *SpawnPlan, _, _ string, _, _ map[string]string) (*operations.AgentChatLaunch, error) {
	s.once.Do(func() { close(s.launched) })
	<-s.hold
	return nil, errors.New("blocking-launch spawner: released at test teardown")
}

func (s *blockingLaunchSpawner) didLaunch() bool {
	select {
	case <-s.launched:
		return true
	default:
		return false
	}
}

// A run whose spawn is PARKED on the execution-slot cap and is terminated
// while it waits used to lose the slot permanently.
//
// runChild did a bare c.slots.Acquire followed by an unconditional
// rt.slot = slotHeld. terminateRun -> releaseSlot, arriving inside that
// window, saw slotFree and released NOTHING; factRunEnded is exactly-once so
// the terminal never ran again; the acquire then landed a real slot and
// stamped it onto a run that had already ended, with nobody left to give it
// back. At the default cap of 4, four such races deadlock the coordinator
// permanently with no diagnostic.
//
// This test is DETERMINISTIC, not racy: the only slot is taken before the
// spawn, so the acquire MUST block; waitForSlotWaiters proves it is parked
// before the terminate fires; and the peer's release afterwards is what lets
// the parked acquire land. Nothing here waits and hopes.
func TestRunChild_TerminateWhileParkedOnCap_DoesNotStrandTheSlot(t *testing.T) {
	resetStrictness(t)
	sp := newBlockingLaunchSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}})
	c := newTestCoordinatorCap(t, sp, nil, 1) // exactly one slot in the whole coordinator

	// A peer occupies the only slot, so the spawn below cannot get one.
	require.True(t, c.slots.TryAcquire(1), "precondition: the single slot starts free")

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "brief", "", "")
	require.NoError(t, err)
	require.True(t, out.Queued, "the spawn must be queued behind the peer, not admitted")

	// runChild is now parked in acquire — proven, not assumed.
	waitForSlotWaiter(t, c.slots)

	// THE RACE: the run's terminal lands while its spawn is still parked.
	c.terminateRun(out.RunID, CauseStopped, "stopped while queued")

	// The peer finishes; the parked acquire lands a real slot.
	c.slots.Release(1)

	// The landed slot must come back to the pool: the run it was acquired for
	// has ended, so nothing else will ever release it.
	require.Eventually(t, func() bool { return slotsIdleWith(c.slots, 1) }, conformanceWait, time.Millisecond,
		"the terminated run must not keep the slot it acquired after its terminal — a free count stuck at 0 "+
			"is a PERMANENT cap shrink, and at the default cap of 4 four of these deadlock the coordinator")

	c.mu.Lock()
	rt := c.byHarp[out.Harp]
	var final slotState
	if rt != nil {
		final = rt.slot
	}
	c.mu.Unlock()
	assert.Equal(t, slotFree, final, "the terminated run's childRt must not be left reading slotHeld")

	assert.False(t, sp.didLaunch(),
		"a run whose terminal already fired must not go on to launch an engine")
}

// acquireRunSlot's three outcomes, forced directly. runChild's full-path test
// above covers one caller end to end; this pins the primitive all three
// (runChild, resumeChild, wakeChild) now share, including the cancelled arm's
// contract that the landed slot is released HERE and not by the caller.
func TestAcquireRunSlot_Outcomes(t *testing.T) {
	t.Run("landed", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinatorCap(t, sp, nil, 1)
		rt := &childRt{harp: "child-a", runID: "run-a"}

		require.NoError(t, c.acquireRunSlot(rt))

		c.mu.Lock()
		state := rt.slot
		c.mu.Unlock()
		assert.Equal(t, slotHeld, state, "a clean acquisition must end at slotHeld")
		assert.True(t, slotsIdleWith(c.slots, 0), "the slot must actually have been taken from the pool")
	})

	t.Run("cancelled by the run terminal mid-wait", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinatorCap(t, sp, nil, 1)
		rt := &childRt{harp: "child-a", runID: "run-a"}

		// A peer holds the only slot, so the acquire below genuinely parks.
		require.True(t, c.slots.TryAcquire(1))

		done := make(chan error, 1)
		go func() { done <- c.acquireRunSlot(rt) }()
		waitForSlotWaiter(t, c.slots)

		c.releaseSlot(rt)  // what terminateRun does
		c.slots.Release(1) // the peer finishes; the parked acquire lands

		select {
		case err := <-done:
			require.ErrorIs(t, err, errSlotClaimCancelled,
				"a claim cancelled mid-wait must be reported, not silently promoted to slotHeld")
		case <-time.After(conformanceWait):
			t.Fatal("acquireRunSlot never returned")
		}

		c.mu.Lock()
		state := rt.slot
		c.mu.Unlock()
		assert.Equal(t, slotFree, state)
		assert.True(t, slotsIdleWith(c.slots, 1),
			"acquireRunSlot itself must hand the unwanted slot back — nobody else can")
	})

	t.Run("acquire fails", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinatorCap(t, sp, nil, 1)
		rt := &childRt{harp: "child-a", runID: "run-a"}
		require.True(t, c.slots.TryAcquire(1))

		done := make(chan error, 1)
		go func() { done <- c.acquireRunSlot(rt) }()
		waitForSlotWaiter(t, c.slots)

		c.cancel() // baseCtx dies under the parked acquire

		select {
		case err := <-done:
			require.Error(t, err)
			assert.False(t, errors.Is(err, errSlotClaimCancelled),
				"a failed acquisition is not a cancelled claim — the callers fail the child on one and stay quiet on the other")
		case <-time.After(conformanceWait):
			t.Fatal("acquireRunSlot never returned")
		}

		c.mu.Lock()
		state := rt.slot
		c.mu.Unlock()
		assert.Equal(t, slotFree, state, "a failed acquisition must leave no claim behind")
	})

	t.Run("already accounted for", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinatorCap(t, sp, nil, 2)
		rt := &childRt{harp: "child-a", runID: "run-a", slot: slotHeld}

		require.NoError(t, c.acquireRunSlot(rt), "an rt that already holds a slot proceeds without taking a second")

		assert.True(t, slotsIdleWith(c.slots, 2), "no second slot may be taken for an rt that already holds one")
	})
}
