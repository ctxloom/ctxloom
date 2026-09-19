package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestRequestRunner_FailsFastOnASessionThatAlreadyFailedItsPending pins the
// window between "the RunnerChannel died" and "a caller that already read the
// session issues its request": failPending SWAPS the pending map out, so a
// waiter registered a moment later lands in the fresh map with nothing left to
// resolve it, and the frame it queued goes to a pump that is gone. The caller
// then waited out the whole default request budget for an answer that could
// never arrive. A dead session must refuse the registration outright.
func TestRequestRunner_FailsFastOnASessionThatAlreadyFailedItsPending(t *testing.T) {
	c := newTestCoordinator(t, researcherSpawner(), nil)

	rs := newRunnerSession("cred-hash-dead", "run-dead", c.now(), func() {})
	c.mu.Lock()
	c.runners["cred-hash-dead"] = rs
	c.mu.Unlock()
	rs.failPending() // the channel's teardown ran

	done := make(chan error, 1)
	go func() {
		_, err := c.requestRunner(context.Background(), "cred-hash-dead",
			&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_KillRun{KillRun: &agentcoordpb.KillRun{RunId: "run-dead"}}})
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a request issued against an ended session must fail, not succeed")
		assert.Contains(t, err.Error(), "ended", "the error must name the session's end, not a timeout")
	case <-time.After(3 * time.Second):
		t.Fatal("requestRunner stalled on a session whose pending map was already swapped out; it can only end at the request budget")
	}
}

// TestFailPending_ResolvesWaitersRegisteredBeforeIt: the fast-fail above must
// not cost the ordinary case — a waiter already in the map when the session
// dies still gets its UNAVAILABLE answer.
func TestFailPending_ResolvesWaitersRegisteredBeforeIt(t *testing.T) {
	c := newTestCoordinator(t, researcherSpawner(), nil)

	rs := newRunnerSession("cred-hash-live", "run-live", c.now(), func() {})
	c.mu.Lock()
	c.runners["cred-hash-live"] = rs
	c.mu.Unlock()

	done := make(chan *agentcoordpb.RunnerResponse, 1)
	go func() {
		resp, _ := c.requestRunner(context.Background(), "cred-hash-live",
			&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_KillRun{KillRun: &agentcoordpb.KillRun{RunId: "run-live"}}})
		done <- resp
	}()

	require.Eventually(t, func() bool {
		rs.reqMu.Lock()
		defer rs.reqMu.Unlock()
		return len(rs.pending) == 1
	}, 3*time.Second, 5*time.Millisecond, "the waiter must register while the session is live")
	rs.failPending()

	select {
	case resp := <-done:
		require.NotNil(t, resp)
		assert.Equal(t, int32(14), resp.GetStatus().GetCode(), "UNAVAILABLE answers a session that ended mid-request")
	case <-time.After(3 * time.Second):
		t.Fatal("a waiter registered before failPending was never resolved")
	}
}
