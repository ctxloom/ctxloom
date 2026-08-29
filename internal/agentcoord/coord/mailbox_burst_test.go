package coord

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// NOTE on assertion style (mailbox_takefail_test.go's cute-brink note): this
// package's Coordinator.Close runs in t.Cleanup and a require.* FailNow inside
// a coord test deadlocks it. assert + return only.

// waitParked blocks until role has a live parked poll, so a test can queue mail
// at the moment a receive is genuinely waiting rather than racing it.
func waitParked(t *testing.T, c *Coordinator, role string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		p := c.polls[role]
		parked := p != nil && !p.done
		c.mu.Unlock()
		if parked {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	t.Errorf("no poll parked for %q within the deadline", role)
	return false
}

// waitPollCleared blocks until role's poll is gone or done — deliverToPoll
// deletes it as it hands the wake over — so a test can tell that the receive
// has PASSED the moment of waking rather than guessing at it.
func waitPollCleared(t *testing.T, c *Coordinator, role string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		p := c.polls[role]
		cleared := p == nil || p.done
		c.mu.Unlock()
		if cleared {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	t.Errorf("poll for %q never cleared; the wake did not fire", role)
	return false
}

// THE SWEEP BURST. A single spoolReactor pass sweeps EVERY child in
// c.spoolRoles and routes their reports SERIALLY — sweepChildOut loops, and
// each entry calls queueMailPayload in turn. So N children finishing during one
// 30s window arrive as N separate queue operations microseconds apart, not as
// one.
//
// Against a parked receive that is fatal: the FIRST queue fires deliverToPoll,
// the poll wakes, and resolvePollWake claims whatever is deliverable AT THAT
// INSTANT — which is one message. The receive returns. Entries 2..N are then
// queued with c.polls[role] empty, so deliverToPoll returns false; and for a
// coordinator's own harp pushMail cannot help either (c.chans has no entry for
// a session owner that never attached a runner). They sit until the coordinator
// happens to call agent_recv again.
//
// Observed in production as "a partial batch delivered, rest left queued": one
// message returned while five more were already waiting, with nothing in the
// response able to say so — agentRecvResult carries only Messages.
//
// This asserts the EFFECT — every message actually returned to the waiting
// caller — not that a notification was emitted.
func TestRecvMail_ASweepBurstIsDeliveredAsOneBatch(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const (
		role  = "parent-wave"
		burst = 6
	)

	type recvOutcome struct {
		msgs []Message
		err  error
	}
	done := make(chan recvOutcome, 1)
	go func() {
		msgs, err := c.recvMail(context.Background(), role, 5*time.Second)
		done <- recvOutcome{msgs: msgs, err: err}
	}()

	if !waitParked(t, c, role) {
		return
	}

	// The first report of the sweep. This is what fires deliverToPoll and wakes
	// the parked receive.
	if _, _, err := c.queueMailPayloadID("m0", "child-0", role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
		return
	}

	// Wait for the wake to actually fire — deliverToPoll deletes the poll — so
	// the receive is provably PAST its first claim before the rest arrive.
	//
	// WITHOUT THIS the test proves nothing: queuing all six in a tight loop
	// lets the woken goroutine claim them all in one pass, which the OLD code
	// does too. The failure being pinned is specifically "claimed what was
	// pending at the instant of waking, then returned", so the rest must arrive
	// strictly after that instant.
	if !waitPollCleared(t, c, role) {
		return
	}
	time.Sleep(15 * time.Millisecond) // the unfixed path has returned by now

	// Entries 2..N of the same reactor pass.
	for i := 1; i < burst; i++ {
		id := fmt.Sprintf("m%d", i)
		if _, _, err := c.queueMailPayloadID(id, fmt.Sprintf("child-%d", i), role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
			return
		}
	}

	select {
	case out := <-done:
		if !assert.NoError(t, out.err) {
			return
		}
		assert.Len(t, out.msgs, burst,
			"a receive woken by a sweep burst must return the WHOLE burst; returning a prefix strands the rest with no signal that they exist")
	case <-time.After(10 * time.Second):
		t.Error("recvMail never returned")
	}
}

// The settle window must not turn an ordinary single arrival into a stall: a
// lone message is still returned, and promptly. This is the control for the
// test above — without it, "returns the whole burst" would be satisfiable by
// simply waiting longer on every receive.
func TestRecvMail_ASingleArrivalStillReturnsPromptly(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "parent-single"

	type recvOutcome struct {
		msgs []Message
		err  error
	}
	done := make(chan recvOutcome, 1)
	started := time.Now()
	go func() {
		msgs, err := c.recvMail(context.Background(), role, 5*time.Second)
		done <- recvOutcome{msgs: msgs, err: err}
	}()

	if !waitParked(t, c, role) {
		return
	}
	if _, _, err := c.queueMailPayloadID("solo", "child-solo", role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
		return
	}

	select {
	case out := <-done:
		if !assert.NoError(t, out.err) {
			return
		}
		assert.Len(t, out.msgs, 1)
		assert.Less(t, time.Since(started), 2*time.Second,
			"a lone arrival must not pay the full wait; the settle window bounds the batch, it does not become the latency")
	case <-time.After(10 * time.Second):
		t.Error("recvMail never returned")
	}
}

// THE SILENCE, COUNTED. pushMail returns without pushing when the recipient has
// no pushable run channel — and a SESSION OWNER never has one, so a
// coordinator's own mail is never pushed and waits for it to call agent_recv
// itself. That return used to be entirely silent: no warn, no counter, nothing.
// A coordinator had no way to learn the wake it was waiting for does not exist
// for it, which is how a missing doorbell reads as "the system is just a bit
// slow" forever.
//
// Asserts the COUNT MOVED, not the log text: the counter is the part an
// observer can poll, and asserting a message would pass on a warn that said
// anything at all.
func TestPushMail_UnpushableRecipientIsCounted(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "owner-with-no-channel"
	before := c.PushUnavailableCount()

	// No run channel was ever registered for this role — the session-owner
	// shape. queueMailPayloadID reaches pushMail once deliverToPoll finds no
	// parked poll.
	if _, _, err := c.queueMailPayloadID("m1", "child-1", role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
		return
	}

	assert.Greater(t, c.PushUnavailableCount(), before,
		"mail that could not be pushed must be COUNTED; a silent return is how a missing wake stays invisible")
}

// The control: a push that CAN happen must not inflate the counter, or the
// count above would be satisfiable by counting every message and would say
// nothing about pushability.
func TestPushMail_DeliveredToAParkedPollIsNotCounted(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "parked-recipient"
	done := make(chan struct{})
	go func() {
		_, _ = c.recvMail(context.Background(), role, 5*time.Second)
		close(done)
	}()
	if !waitParked(t, c, role) {
		return
	}
	before := c.PushUnavailableCount()

	if _, _, err := c.queueMailPayloadID("m1", "child-1", role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
		return
	}
	<-done

	assert.Equal(t, before, c.PushUnavailableCount(),
		"a parked poll took the message, so nothing was unpushable")
}
