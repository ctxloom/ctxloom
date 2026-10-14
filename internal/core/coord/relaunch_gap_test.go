package coord

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEndRun_ExplicitSendInTheRelaunchGapLaunchesOnce forces the window
// between endRun's claimRunTerminal and its leftover-mail tail
// (relaunchForLeftoverMail). Two parties can arm a resume FOR THE SAME ended
// run there, and nothing upstream of resumeChild keeps them apart:
//
//   - an explicit agent_send observes the harp ended, so driveObserved clears
//     the launch gate and dispatches a resume (no claimLeftover — an explicit
//     ask is not the bounded tail);
//   - endRun, released, finds that same mail pending, wins claimLeftover
//     (nobody else claimed it), spends a relaunch and dispatches a second
//     resume, armed for the same run.
//
// Both attempts are parked in Resolve until BOTH are there, so both have
// already passed resumableRun's generation check (the harp's current run is
// still the ended one) — the only thing left between them and two launches
// is the claim inside enqueueRun, which re-reads the harp's current run in the
// journal's single-writer window and refuses a resume whose run is no longer
// ended (errResumeLost). This test is the guard on that claim.
func TestEndRun_ExplicitSendInTheRelaunchGapLaunchesOnce(t *testing.T) {
	resetStrictness(t)
	const slotCap = 2 // room for a second launch, so a double launch shows as one, not as a hang
	sp := startRunSpawner(t, nil)

	var target atomic.Value
	target.Store("")
	parked, hold := make(chan struct{}), make(chan struct{})
	var parkOnce, releaseOnce, resolveOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	resolveGate := make(chan struct{})
	openResolve := func() { resolveOnce.Do(func() { close(resolveGate) }) }

	c := newTestCoordinatorWith(t, sp, func(o *Options) {
		o.ConcurrencyCap = slotCap
		o.endRecordedHook = func(runID string) {
			if runID == target.Load().(string) {
				parkOnce.Do(func() { close(parked) })
				<-hold
			}
		}
	})
	// After the coordinator's cleanup is registered, so these run first: a
	// parked end or resolve would otherwise stall Close.
	t.Cleanup(openResolve)
	t.Cleanup(release)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	harp, ended := out.Harp, out.RunID
	require.Equal(t, 1, sp.chatCount())

	// Every resume attempt from here on parks in Resolve and says so.
	resolveEntered := make(chan string, 8)
	sp.mu.Lock()
	sp.resolveEntered, sp.resolveGate = resolveEntered, resolveGate
	sp.mu.Unlock()
	target.Store(ended)

	// 1. End the run; endRun parks with the end journaled, its tail to come.
	require.NoError(t, c.recordSummary(harp, ended, 1, finalSummary("FINAL: done")))
	within(t, parked, "the run's end was never journaled")
	require.Equal(t, StateEnded, rosterState(c, harp), "premise: the roster reads the run ended")

	// 2. The explicit send lands in the gap and arms resume A for the ended run.
	_, err = c.AgentSend(ownerIdentity(), harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	awaitResolve(t, resolveEntered, "the explicit send never dispatched a resume")

	// 3. endRun resumes; its leftover-mail tail finds the same mail pending
	// and arms resume B for the same ended run.
	release()
	awaitResolve(t, resolveEntered, "endRun's leftover-mail tail never dispatched its own resume (premise: both parties arm)")
	require.Equal(t, ended, currentRunID(c, harp), "premise: neither attempt has enqueued — both passed resumableRun")

	// 4. Both race enqueueRun's claim.
	openResolve()
	awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	require.NoError(t, c.awaitChildUp(awaitCtx, harp), "every armed attempt settles")
	cancel()
	awaitChatText(t, sp, 1, "one more thing")

	assert.Equal(t, 2, sp.chatCount(), "one ended run, one resume: exactly one spawner Start beyond the original — %s", launchDiagnostic(c, sp, harp))
	assert.Equal(t, 1, liveRunsOf(c, harp), "exactly one live run for the session")
	assert.NotEqual(t, ended, currentRunID(c, harp))
	held := heldSlots(c)
	assert.LessOrEqual(t, held, 1)
	assert.True(t, slotsIdleWith(c.slots, int64(slotCap-held)), "the slot pool holds exactly cap minus what live runs hold — no slot leaked (held=%d)", held)
	delivered := 0
	for i := range sp.chatCount() {
		delivered += countChatText(sp, i, "one more thing")
	}
	assert.Equal(t, 1, delivered, "the mail is delivered exactly once — %s", launchDiagnostic(c, sp, harp))
	c.mu.Lock()
	relaunches := c.launchGateLocked(harp).relaunches
	c.mu.Unlock()
	assert.LessOrEqual(t, relaunches, 1, "the tail spends at most one relaunch for the gap; the explicit send spends none")
}

// awaitResolve waits for one resume attempt to reach the fake's Resolve.
func awaitResolve(t *testing.T, ch <-chan string, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(conformanceWait):
		t.Fatal(msg)
	}
}

// liveRunsOf counts harp's runs the journal holds as not ended.
func liveRunsOf(c *Coordinator, harp string) int {
	n := 0
	c.runs.View(func() {
		for _, r := range c.runsF.runs {
			if r.Harp == harp && !r.Ended {
				n++
			}
		}
	})
	return n
}

// heldSlots counts the published runs whose bookkeeping holds a slot.
func heldSlots(c *Coordinator) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, rt := range c.attach {
		if rt.slot == slotHeld {
			n++
		}
	}
	return n
}
