package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The RunChannel's receive goroutine outlives RunChannel's return, and
// neither severChan nor terminateRun synchronises with it — so a frame already in
// flight when a channel is severed is still dispatched. handleCustomEvent then
// runs the turn-state arms for a run that has ALREADY ended, and c.byHarp still
// holds that run's childRt (it is never deleted).
//
// onTurnStarted was the expensive one: it would claim and tryAcquire an execution
// slot for the ended run, and nothing would ever release it — terminateRun's own
// release has already happened — so the execution cap shrinks by one for the rest
// of the process's life, permanently, once per racing frame.
func TestOnTurnStarted_AfterTheRunsTerminal_DoesNotLeakAnExecutionSlot(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinatorCap(t, sp, nil, 2)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)

	c.terminateRun(out.RunID, CauseStopped, "test terminal")
	require.Eventually(t, func() bool { return c.runEnded(out.RunID) }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool { return slotsIdleWith(c.slots, 2) }, conformanceWait, 10*time.Millisecond,
		"precondition: the terminal gives the slot back")

	// THE RACE: a ctxloom/turn_started frame that was already on the wire when
	// the channel was severed reaches handleCustomEvent now.
	c.onTurnStarted(out.Harp)

	assert.True(t, slotsIdleWith(c.slots, 2),
		"a turn-start for an ENDED run must not take a slot: nothing would ever release it, and the execution cap would shrink permanently")

	c.mu.Lock()
	rt := c.byHarp[out.Harp]
	c.mu.Unlock()
	require.NotNil(t, rt, "the ended run's childRt is still registered — that is why the guard is needed")
	c.mu.Lock()
	slotState := rt.slot
	c.mu.Unlock()
	assert.Equal(t, slotFree, slotState, "the ended run must end up owning no slot")
}

// The same race through onTurnIdle: it bridges the turn's result to the parent,
// and on an empty accumulator bridgeTurnResult queues an "error" message saying
// the turn produced no output. Delivered after the run's terminal, that is a
// second, contradictory report about a run the parent has already been told
// ended.
func TestOnTurnIdle_AfterTheRunsTerminal_DoesNotBridgeAgain(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)

	c.terminateRun(out.RunID, CauseStopped, "test terminal")
	require.Eventually(t, func() bool { return c.runEnded(out.RunID) }, conformanceWait, 10*time.Millisecond)

	// Drain whatever the terminal itself legitimately delivered.
	recvKind(t, c, KindExited, conformanceWait)

	c.onTurnIdle(out.Harp)

	assertNoMailKind(t, c, "error", 300*time.Millisecond)
}
