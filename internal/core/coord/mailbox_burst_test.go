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
		c.inbox.mu.Lock()
		p := c.inbox.polls[role]
		parked := p != nil && !p.done
		c.inbox.mu.Unlock()
		if parked {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	t.Errorf("no poll parked for %q within the deadline", role)
	return false
}

// THE SWEEP BURST. A single spoolReactor pass sweeps EVERY child in
// c.spoolRoles and routes their reports SERIALLY, so N children finishing
// during one window arrive as N deliveries microseconds apart. Against a
// parked receive the FIRST fires the wake; the woken receive claims whatever
// is on disk AT THAT INSTANT and returns.
//
// There is no settling window to race: what lands after the claim is
// deliverable to the NEXT receive, which returns it WITHOUT parking (a
// receive begins with a read). The ordering is forced — the rest of the
// burst is queued only once the first receive has provably returned — and
// the effect is asserted: every message reaches the caller across the two
// receives, and the second costs no wait at all.
func TestRecvMail_MailLandingAfterAClaimReturnsOnTheNextReceiveWithoutParking(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const (
		role  = "coordinator-harp"
		burst = 6
	)

	type recvOutcome struct {
		msgs []Message
		err  error
	}
	done := make(chan recvOutcome, 1)
	go func() {
		msgs, err := c.inbox.recv(context.Background(), role, 5*time.Second)
		done <- recvOutcome{msgs: msgs, err: err}
	}()
	if !waitParked(t, c, role) {
		return
	}

	// The first report of the sweep: what wakes the parked receive.
	if _, err := c.queueMailPayloadID("m0", "child-0", role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
		return
	}
	var first recvOutcome
	select {
	case first = <-done:
	case <-time.After(10 * time.Second):
		t.Error("the woken receive never returned")
		return
	}
	if !assert.NoError(t, first.err) || !assert.Len(t, first.msgs, 1, "the wake claims what was on disk at that instant") {
		return
	}

	// Entries 2..N of the same reactor pass, landing strictly after the first
	// receive has returned.
	for i := 1; i < burst; i++ {
		id := fmt.Sprintf("m%d", i)
		if _, err := c.queueMailPayloadID(id, fmt.Sprintf("child-%d", i), role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
			return
		}
	}
	assert.False(t, c.inbox.parked(role), "nothing is parked between the two receives: the reminder path, not a settle, covers this window")

	rest, err := c.inbox.recv(context.Background(), role, 0)
	if !assert.NoError(t, err, "a zero-wait receive returns what landed since the last claim without parking") {
		return
	}
	assert.Len(t, rest, burst-1, "the rest of the burst is the next receive's, whole")
}

// A lone arrival is returned promptly: the woken receive claims and returns,
// it does not wait for company.
func TestRecvMail_ASingleArrivalStillReturnsPromptly(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	role := ownerIdentity().Harp

	type recvOutcome struct {
		msgs []Message
		err  error
	}
	done := make(chan recvOutcome, 1)
	started := time.Now()
	go func() {
		msgs, err := c.inbox.recv(context.Background(), role, 5*time.Second)
		done <- recvOutcome{msgs: msgs, err: err}
	}()

	if !waitParked(t, c, role) {
		return
	}
	if _, err := c.queueMailPayloadID("solo", "child-solo", role, "result", "FINAL: done", nil, ""); !assert.NoError(t, err) {
		return
	}

	select {
	case out := <-done:
		if !assert.NoError(t, out.err) {
			return
		}
		assert.Len(t, out.msgs, 1)
		assert.Less(t, time.Since(started), 2*time.Second,
			"a lone arrival must not pay the full wait")
	case <-time.After(10 * time.Second):
		t.Error("recvMail never returned")
	}
}
