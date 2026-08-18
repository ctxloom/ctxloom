package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// NOTE on assertion style (mailbox_takefail_test.go's cute-brink note): this
// package's Coordinator.Close runs in t.Cleanup and a require.* FailNow inside
// a coord test deadlocks it. assert + return only.

// A recv whose CLIENT went away (ctx cancelled) but which LOST the
// race to a concurrent delivery consumed the message anyway. abandonPoll saw
// p.done, waited for the delivery goroutine, and returned the message to a
// caller that no longer exists — and the id stayed in c.delivered, where the
// next recv's cursor-ack (ackDelivered) journals a mail-consumed fact for it.
// The message is then GONE: acked as delivered, never seen by anybody, and
// undeliveredLocked filters it out forever.
//
// At-least-once means a delivery nobody received is re-delivered. A caller that
// is gone received nothing, so the reservation must be released.
func TestRecvCancelled_DeliveryThatWonTheRaceStaysDeliverable(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-a"
	if _, _, err := c.queueMailPayloadID("m1", "parent", role, "task", "do the thing", nil, ""); !assert.NoError(t, err) {
		return
	}
	if !assert.Equal(t, 1, c.pendingCount(role), "precondition: the message is deliverable") {
		return
	}

	// Park a poll exactly as recvMail does, then let a delivery WIN it — a
	// bare wake now (B2): deliverToPoll no longer reserves anything, so the
	// message stays fully pending until something actually CLAIMS it.
	p := &parkedPoll{ch: make(chan pollResult, 1)}
	c.mu.Lock()
	c.polls[role] = p
	c.mu.Unlock()

	if !assert.True(t, c.deliverToPoll(role), "precondition: the wake reaches the parked poll") {
		return
	}
	if !assert.Equal(t, 1, c.pendingCount(role), "precondition: a bare wake does not reserve anything") {
		return
	}

	// ...and only now does the client's context die.
	msgs, err := c.abandonPoll(role, p, context.Canceled, true)

	assert.Empty(t, msgs, "nothing may be handed to a caller that is gone")
	assert.ErrorIs(t, err, context.Canceled, "the cancellation must be reported, not masked by the delivery")
	assert.Equal(t, 1, c.pendingCount(role),
		"a caller that is gone must not CLAIM the wake on its own behalf — otherwise the next "+
			"recv's cursor-ack journals a consume for a message nobody ever received")

	// And it really is deliverable again: a fresh recv returns it.
	got, rerr := c.recvMail(context.Background(), role, 0)
	if !assert.NoError(t, rerr) {
		return
	}
	if !assert.Len(t, got, 1) {
		return
	}
	assert.Equal(t, "do the thing", got[0].Body)
}

// The register claimed deliverToPoll's goroutine (which calls
// onRoleUnpark, and so may block on the execution-slot semaphore) makes the
// recv's documented bounded `wait` unbounded, and implied the wait should
// simply expire.
//
// It must NOT. Once deliverToPoll has flipped p.done this poll OWNS the wake:
// abandonPoll's job is to redeem it into an actual claim before reporting the
// timeout. A timeout that returned ErrRecvTimeout at that point would strand
// the message in exactly the way the take-fail path documented. The overshoot is the
// execution-slot cap doing its job — a parked child may not resume an
// EXECUTING turn without a slot — so the invariant is that a delivery which won
// the race is delivered, however late the timer was.
//
// This pins that invariant: apply the "make the deadline authoritative" fix the
// row implies and this goes red.
func TestRecvTimeout_DeliveryThatWonTheRaceIsStillDelivered(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-b"
	if _, _, err := c.queueMailPayloadID("m9", "parent", role, "task", "already yours", nil, ""); !assert.NoError(t, err) {
		return
	}

	p := &parkedPoll{ch: make(chan pollResult, 1)}
	c.mu.Lock()
	c.polls[role] = p
	c.mu.Unlock()

	if !assert.True(t, c.deliverToPoll(role)) {
		return
	}

	// The timer fires AFTER the delivery claimed the poll (callerGone == false:
	// the recv's own caller is still there waiting for an answer).
	msgs, err := c.abandonPoll(role, p, ErrRecvTimeout, false)

	assert.NoError(t, err, "a delivery that already won must not be reported as a timeout")
	if !assert.Len(t, msgs, 1, "the reserved message must reach its recv, not be stranded by the timer") {
		return
	}
	assert.Equal(t, "already yours", msgs[0].Body)
	assert.Equal(t, 0, c.pendingCount(role),
		"the message stays reserved for the recv that received it (the cursor-ack consumes it)")
}
