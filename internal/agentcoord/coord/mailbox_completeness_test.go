package coord

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// DELIVERY COMPLETENESS under overlapping receives.
//
// NOTE on assertion style (mailbox_takefail_test.go's note): this package's
// Coordinator.Close runs in t.Cleanup and a require.* FailNow inside a coord
// test deadlocks it. assert + return only.
//
// Why these tests exist, and why they assert what they do. The bus's recorded
// failure was not "a message was slow" but "a message was CONSUMED and never
// delivered": at-least-once degraded to at-most-once. A test that only asserts
// "something arrived" is satisfied by a system that delivers one message and
// drops a different one, which is exactly the shape of the incident. So every
// test below sends a KNOWN SET and asserts the received multiset is that set,
// each element EXACTLY ONCE, with nothing left pending.
//
// The overlap is FORCED, never raced for. "One active long-poll per role,
// newest preempts" (recvMail) is what the MCP call budget produces in
// production: a long agent_recv is auto-backgrounded, its caller stops reading
// the response, and the same session issues a newer agent_recv. That is
// reproduced here by parking a poll whose channel NOBODY EVER DRAINS and
// letting a delivery win it, then issuing the newer receive — no sleeps, no
// hoping for an interleaving.

// TestRecvOverlapping_EveryQueuedMessageIsReceivedExactlyOnce drives a whole
// SEQUENCE of messages through the orphaned-poll/newer-recv cycle, one per
// message, and pins the property the single-message case cannot: across
// repeated preemptions the receiver sees every message once — not "at least
// one arrived", and not "the last one arrived".
//
// The cursor-ack is load-bearing here and is exercised on purpose: each
// iteration's recvMail begins with ackDelivered, which durably consumes what
// the PREVIOUS iteration claimed. That is the write that turned a delivery
// nobody received into a permanently invisible message, so a regression in it
// shows up as a hole in the middle of this sequence rather than as a failure
// on the first message.
func TestRecvOverlapping_EveryQueuedMessageIsReceivedExactlyOnce(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-completeness"
	const n = 6

	sent := make([]string, 0, n)
	received := map[string]int{}
	bodies := map[string]string{}

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("m%d", i)
		body := fmt.Sprintf("report %d", i)
		if _, _, err := c.queueMailPayloadID(id, "parent", role, KindResult, body, nil, ""); !assert.NoError(t, err) {
			return
		}
		sent = append(sent, id)
		bodies[id] = body

		// The auto-backgrounded receive: a poll that is parked server-side and
		// whose caller will never read the channel. Let the delivery win it.
		p := &parkedPoll{ch: make(chan pollResult, 1)}
		c.mu.Lock()
		c.polls[role] = p
		c.mu.Unlock()
		if !assert.True(t, c.deliverToPoll(role), "precondition %d: the wake must reach the parked poll", i) {
			return
		}

		// The newer receive for the same role.
		msgs, err := c.recvMail(context.Background(), role, 0)
		if !assert.NoError(t, err, "message %d was handed to an orphaned poll and must still reach the next recv", i) {
			return
		}
		for _, m := range msgs {
			received[m.ID]++
			assert.Equal(t, bodies[m.ID], m.Body, "message %s arrived with the wrong body", m.ID)
		}
	}

	// COMPLETENESS, both directions: nothing lost and nothing duplicated.
	for _, id := range sent {
		assert.Equal(t, 1, received[id],
			"message %s must be received EXACTLY once (0 = lost by the orphaned poll's cursor-ack, >1 = redelivered after a claim)", id)
	}
	assert.Len(t, received, n, "the receiver saw ids it was never sent: %v", received)
	assert.Equal(t, 0, c.pendingCount(role), "every message was received, so nothing may still be pending")
}

// TestRecvPreempted_ConcurrentOverlapLosesNoMessage runs two receives that
// genuinely overlap in wall time — the older still parked when the newer
// arrives — and pins that the preemption itself neither consumes nor
// duplicates anything.
//
// The overlap is established by OBSERVED STATE, not by sleeping: the test
// waits until the older receive's poll is actually registered in c.polls, and
// then until that receive has returned ErrRecvPreempted. Only after the
// preemption has demonstrably happened is any mail queued, so the messages
// cannot be delivered by an interleaving the test did not intend.
func TestRecvPreempted_ConcurrentOverlapLosesNoMessage(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-overlap"
	const n = 4

	// The older receive parks and is then abandoned by its caller.
	olderErr := make(chan error, 1)
	go func() {
		_, err := c.recvMail(context.Background(), role, conformanceWait)
		olderErr <- err
	}()
	if !assert.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.polls[role] != nil
	}, conformanceWait, time.Millisecond, "the older receive never parked") {
		return
	}

	// The newer receive preempts it. Wait for the older one to have COMPLETED
	// with the typed preemption error before queueing anything: that is what
	// makes "the mail arrived after the preemption" a fact rather than a race.
	newer := make(chan []Message, 1)
	go func() {
		msgs, err := c.recvMail(context.Background(), role, conformanceWait)
		if err != nil {
			newer <- nil
			return
		}
		newer <- msgs
	}()
	select {
	case err := <-olderErr:
		if !assert.ErrorIs(t, err, ErrRecvPreempted, "the older receive must complete with the typed preemption error") {
			return
		}
	case <-time.After(conformanceWait):
		assert.Fail(t, "the older receive was never preempted")
		return
	}

	sent := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("o%d", i)
		if _, _, err := c.queueMailPayloadID(id, "parent", role, KindResult, fmt.Sprintf("finding %d", i), nil, ""); !assert.NoError(t, err) {
			return
		}
		sent = append(sent, id)
	}

	received := map[string]int{}
	select {
	case msgs := <-newer:
		if !assert.NotEmpty(t, msgs, "the surviving receive must be completed by the queued mail") {
			return
		}
		for _, m := range msgs {
			received[m.ID]++
		}
	case <-time.After(conformanceWait):
		assert.Fail(t, "the surviving receive was never completed")
		return
	}

	// Drain the remainder. Each call also cursor-acks the previous batch, so a
	// message the preemption had wrongly reserved would be journaled consumed
	// here and never appear.
	deadline := time.Now().Add(conformanceWait)
	for len(received) < n && time.Now().Before(deadline) {
		msgs, err := c.recvMail(context.Background(), role, 10*time.Millisecond)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			received[m.ID]++
		}
	}

	for _, id := range sent {
		assert.Equal(t, 1, received[id], "message %s must survive the preemption exactly once", id)
	}
	assert.Len(t, received, n, "the receiver saw ids it was never sent: %v", received)
	assert.Equal(t, 0, c.pendingCount(role), "every message was received, so nothing may still be pending")
}
