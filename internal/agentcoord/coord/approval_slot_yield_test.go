package coord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A finding claimed a DECIDED approval "can be blocked indefinitely by peer
// slot contention": serveApproval parks the child (yielding its execution
// slot) for the relay, and onRoleUnpark then makes a blocking
// turnSlots.acquire before the decision can be returned.
//
// The mechanism is real; the defect is not. It is the execution-slot cap
// doing its job — a child that resumes an EXECUTING turn must hold a slot,
// and the wait is bounded by executing peers finishing, because a parked
// child holds no slot and so cannot be part of what it is waiting on. The
// project has already ruled on the identical claim in the recv-park path
// (TestRecvTimeout_DeliveryThatWonTheRaceIsStillDelivered): the
// invariant is that a decision which won the race IS delivered, however late
// — not that the wait should expire.
//
// This is the approval-path twin of that pin. Apply the fix the row implies
// (let the reacquire give up, or return the decision without one) and either
// the decision is dropped or the child resumes an executing turn over the
// cap; both are visible here.
func TestApproval_DecisionSurvivesSlotContention(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-1")
	// Explicit relay_to_role ladder, not the plan preset (relayLadderSpawner):
	// this test's subject is the SLOT-YIELD mechanism during any parked
	// approval, not which preset selects relay_to_role — the plan preset no
	// longer relays a COMMAND_EXECUTION directly since marauding-hacksaw.
	sp := relayLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
	c := newTestCoordinatorCap(t, sp, nil, 1) // exactly one slot, so contention is forced

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	msgs := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, msgs, 1, "the relay must land in the parent's mailbox")

	// The park must have YIELDED the child's slot: a child waiting on a human
	// consumes no compute and must not starve its peers up to the ceiling.
	require.Eventually(t, func() bool {
		c.slots.mu.Lock()
		defer c.slots.mu.Unlock()
		return c.slots.free == 1
	}, conformanceWait, 10*time.Millisecond,
		"a child parked on an approval must hold no execution slot")

	// Someone else takes the freed slot, so the child's reacquire below has
	// to block — the exact contention the row is about.
	require.True(t, c.slots.tryAcquire(), "the yielded slot must be takeable by a peer")

	decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "reviewed", decision, msgs[0].ID)
	require.NoError(t, err)

	// The decision is made, but the child may not resume an EXECUTING turn
	// while the cap is full. It waits.
	time.Sleep(250 * time.Millisecond)
	if sc := sp.chat(0); sc != nil {
		assert.Empty(t, sc.recordedAnswers(),
			"a decided approval must not resume the child's turn over the execution-slot cap")
	}

	// The peer finishes; the waiting child gets its slot and its decision —
	// which was never lost, only deferred.
	c.slots.release()
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond,
		"a decision that won the race is delivered however late the slot arrives")
	assert.Equal(t, "allow-1", sp.chat(0).recordedAnswers()[0].OptionID)
}
