package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// U024-F08: respond blocked its CALLER for up to five seconds on a full send
// pump, and respond runs on the channel's own RECEIVE goroutine for two paths —
// a request arriving with no request_id, and re-delivery of a cached response to
// a reissue. Stalling there stops every inbound frame on that channel: acks,
// mail-consumption facts, park and turn-state transitions, all behind one slow
// writer. Its drop notice was wrong about the consequence too, promising "the
// runner reissues on reconnect" when a LIVE channel's request simply fails at
// Home.Request's own budget.
func TestRespond_FullPumpDoesNotStallTheCaller(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	// Unbuffered and never read: the pump is as saturated as it gets.
	ch := &runChan{role: "child-slow", id: Identity{Harp: "child-slow", RunID: "run-slow"},
		send: make(chan *agentcoordpb.CoordinatorFrame), completed: make(chan struct{})}

	start := time.Now()
	c.respond(ch, &agentcoordpb.CoordinatorResponse{Status: okStatus("")})
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second,
		"respond must hand the bounded wait off, not hold the receive goroutine for %s", responseQueueWindow)
}

// The other half: handing the wait off is what makes it useful. A pump that
// drains inside the window now DELIVERS the response instead of dropping it —
// asserted on the frame arriving, not on respond's return.
func TestRespond_ResponseIsDeliveredOnceTheFullPumpDrains(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	send := make(chan *agentcoordpb.CoordinatorFrame, 1)
	send <- &agentcoordpb.CoordinatorFrame{} // occupy the only slot
	ch := &runChan{role: "child-drain", id: Identity{Harp: "child-drain", RunID: "run-drain"},
		send: send, completed: make(chan struct{})}

	c.respond(ch, &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "the payload")})

	<-send // the writer pump catches up
	select {
	case frame := <-send:
		require.NotNil(t, frame.GetResponse(), "the queued frame must be the response, not a placeholder")
		assert.Equal(t, "the payload", frame.GetResponse().GetStatus().GetMessage())
	case <-time.After(2 * time.Second):
		t.Fatal("the response was never delivered after the pump drained")
	}
}

// handleAgentRequest's cached-response re-delivery is one of the two paths that
// runs on the receive goroutine, so it must return promptly even when the pump
// is full: an approval relay's reissue after a reconnect must not freeze the new
// channel's inbound traffic.
func TestHandleAgentRequest_CachedRedeliveryDoesNotStallOnAFullPump(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	role := "child-reissue"
	ch := &runChan{role: role, id: Identity{Harp: role, RunID: "run-reissue"},
		send: make(chan *agentcoordpb.CoordinatorFrame), completed: make(chan struct{})}
	c.mu.Lock()
	c.chans[role] = ch
	c.reqTrack = map[reqKey]*inflightReq{
		{role: role, reqID: "req-1"}: {resp: &agentcoordpb.CoordinatorResponse{RequestId: "req-1", Status: okStatus("already answered")}},
	}
	c.mu.Unlock()

	start := time.Now()
	c.handleAgentRequest(ch, &agentcoordpb.AgentRequest{RequestId: "req-1"})
	assert.Less(t, time.Since(start), time.Second,
		"a reissue whose answer is already cached must not block the receive loop on a full pump")
}
