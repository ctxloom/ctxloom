package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests below each seal the coordinator's tracked group (what Close does
// first) and THEN trigger one dispatching call, pinning what that call does
// with the refusal: a sealed group runs nothing, so every caller must turn
// the refusal into an outcome a waiter can see.

// TestStartDrain_RefusedOnceSealedSettlesEveryChildInterrupted: a drain whose
// runner is refused still settles — a BeginDrain caller waits on Done — and
// accounts for every child it covered the way drainWait reports a child
// Close overtook: interrupted.
func TestStartDrain_RefusedOnceSealedSettlesEveryChildInterrupted(t *testing.T) {
	c := &Coordinator{drains: make(map[*Drain]struct{})}
	c.tracked.Seal()
	d := newDrain(drainPolicy{}, []drainChild{{harp: "child-b", runID: "run-b"}, {harp: "child-a", runID: "run-a"}})

	c.startDrain(d)

	select {
	case <-d.Done():
	default:
		t.Fatal("a refused drain never settled; its waiter hangs")
	}
	assert.Equal(t, DrainOutcome{Interrupted: []string{"child-a", "child-b"}}, d.Outcome())
	c.drainMu.Lock()
	_, live := c.drains[d]
	c.drainMu.Unlock()
	assert.False(t, live, "a refused drain is still registered live")
}

// TestDriveObserved_ResumeRefusedOnceSealedKeepsTheMailQueued: a delivery to
// an ended harp would resume it; refused, the launch attempt it armed is
// settled (nothing waits on an attempt that never runs), the disposition says
// the resume did not happen, and the mail stays queued for the next run.
func TestDriveObserved_ResumeRefusedOnceSealedKeepsTheMailQueued(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	// Sealed BEFORE anything is dispatched, so even the spool reactor's own
	// sweep can reach only refused dispatches. The refused agent_run leaves a
	// tracked harp (TestAgentRun_RefusedOnceSealedLeavesTheRunEnqueued) for
	// the mail to be queued to; driveObserved acts on the state it is handed.
	c.tracked.Seal()
	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.ErrorIs(t, err, ErrGroupSealed)
	require.Len(t, sp.assignedSessions(), 1)
	harp := sp.assignedSessions()[0]
	rec := c.currentRunRecord(harp)
	require.NotNil(t, rec)
	_, err = c.queueMail(ownerIdentity().Harp, harp, KindMessage, "more work")
	require.NoError(t, err)

	assert.Equal(t, deliveryEndedDraining, c.driveObserved(harp, StateEnded, rec.RunID))

	assert.Equal(t, 1, c.pendingCount(harp), "the mail the refused resume was for is no longer queued")
	c.mu.Lock()
	armed := append([]chan struct{}(nil), c.launchArmed[harp]...)
	c.mu.Unlock()
	require.NotEmpty(t, armed)
	for _, ch := range armed {
		select {
		case <-ch:
		default:
			t.Fatal("a refused resume left its launch attempt armed and unsettled")
		}
	}
	assert.Len(t, sp.assignedSessions(), 1, "a refused resume launched a session")
}

// TestHandleRequest_RefusedOnceSealedAnswersWithoutCaching: a plane-2 request
// arriving as Close begins is answered with the refusal on its channel, and its
// in-flight record is dropped — not cached as the reply, and not left
// "in flight" for a reissue to wait on forever.
func TestHandleRequest_RefusedOnceSealedAnswersWithoutCaching(t *testing.T) {
	c := &Coordinator{}
	c.tracked.Seal()
	ch := &RunChannel{BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](func() {}, 4), role: "child-a", id: Identity{Harp: "child-a"}}

	c.HandleRequest(ch, AgentRequest{RequestID: "req-1", Kind: RosterRequest{}})

	select {
	case f := <-ch.send:
		require.NotNil(t, f.Reply, "the channel got a frame that is not a reply")
		assert.Equal(t, "req-1", f.Reply.RequestID)
		assert.ErrorIs(t, f.Reply.Err, ErrGroupSealed)
	default:
		t.Fatal("a refused request was not answered")
	}
	c.mu.Lock()
	_, tracked := c.reqTrack[reqKey{role: "child-a", reqID: "req-1"}]
	c.mu.Unlock()
	assert.False(t, tracked, "a refused request is still tracked, so a reissue would wait on it or get the cached refusal")
}

// TestAgentRun_RefusedOnceSealedLeavesTheRunEnqueued: agent_run's dispatch of
// the child is refused after run.enqueued is durable. The caller is told it
// did not launch, and the run stays enqueued — the state a coordinator dying
// between enqueue and launch leaves, which adoption answers — with its harp
// still assigned.
func TestAgentRun_RefusedOnceSealedLeavesTheRunEnqueued(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	c.tracked.Seal()

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.ErrorIs(t, err, ErrGroupSealed)
	assert.Nil(t, out)

	assigned := sp.assignedSessions()
	require.Len(t, assigned, 1)
	rec := c.currentRunRecord(assigned[0])
	require.NotNil(t, rec, "the run was never enqueued")
	assert.False(t, rec.Ended, "a refused launch ended the run")
	assert.Equal(t, StateQueued, c.runState(rec.RunID))
	assert.Empty(t, sp.endedSessions(), "the enqueued run's harp was released")
}
