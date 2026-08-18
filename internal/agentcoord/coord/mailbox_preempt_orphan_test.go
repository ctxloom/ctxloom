package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// NOTE on assertion style (mailbox_takefail_test.go's cute-brink note): this
// package's Coordinator.Close runs in t.Cleanup and a require.* FailNow inside
// a coord test deadlocks it. assert + return only.

// THE DECISIVE EXPERIMENT (B2). "One active long-poll per role, newest
// preempts" (mailbox.go's recvMail) is exactly the shape the MCP call budget
// forces in production: an agent_recv the client gave up waiting on (but
// never cancelled) is still a live parked poll server-side, and the same
// session issuing agent_recv again is a SECOND, NEWER call for the same role.
//
// Park a poll, let a delivery WIN it (deliverToPoll), and — crucially — never
// drain that poll's channel (the auto-backgrounded call whose response nobody
// is left to read). Then issue a second recv for the same role, exactly as
// "newest preempts" describes.
//
// Today, deliverToPoll RESERVES the message id in c.delivered the instant it
// hands the message to the older poll's channel. The second recv's very first
// step, ackDelivered, reads c.delivered and journals a factMailConsumed for
// it — treating "handed to a channel" as proof of receipt, when nothing has
// proven the older poll's caller ever read that channel. The message is
// durably consumed, filtered out of undeliveredLocked forever, and the second
// recv comes back with nothing: acked as delivered, never seen by anybody.
//
// This is the mailbox-plane twin of spooldelivery.go's stated invariant
// ("CONSUMPTION IS A RENAME, and the rename is the ACK... a later Recv proved
// the harness took the batch") — except here the "rename" (ackDelivered's
// journal write) fires on ANY subsequent recv for the role, not on proof that
// the specific delivery was ever returned to a live caller.
func TestRecvPreempted_DeliveryToAnOrphanedPollIsNotLost(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-orphan"
	if _, _, err := c.queueMailPayloadID("m1", "parent", role, "task", "do the thing", nil, ""); !assert.NoError(t, err) {
		return
	}
	if !assert.Equal(t, 1, c.pendingCount(role), "precondition: the message is deliverable") {
		return
	}

	// Park a poll exactly as recvMail does, then let a delivery WIN it —
	// simulating the auto-backgrounded first agent_recv whose channel nobody
	// is ever going to drain.
	p := &parkedPoll{ch: make(chan pollResult, 1)}
	c.mu.Lock()
	c.polls[role] = p
	c.mu.Unlock()

	msg := Message{ID: "m1", From: "parent", To: role, Kind: "task", Body: "do the thing"}
	if !assert.True(t, c.deliverToPoll(role, msg), "precondition: the delivery wins the parked poll") {
		return
	}

	// A newer recv now issues for the SAME role — "one active long-poll per
	// role, newest preempts" — while the older poll's delivery sits
	// undrained.
	got, err := c.recvMail(context.Background(), role, 0)

	assert.NoError(t, err, "a message handed to an orphaned poll must still reach the next recv, not vanish as an ack for a delivery nobody received")
	if !assert.Len(t, got, 1, "the batch delivered to the abandoned poll must not be silently lost") {
		return
	}
	assert.Equal(t, "do the thing", got[0].Body)
}
