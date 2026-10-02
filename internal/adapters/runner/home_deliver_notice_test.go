package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// newNoticeHome builds the minimum Home deliverNotice touches: the dedupe maps,
// a context for the turn-queue send, and no pump on turnQ so the test can read
// the queue itself rather than race a sink. Its owner is present — the link is
// up — which is the state every notice here is delivered in.
func newNoticeHome(t *testing.T) *Home {
	t.Helper()
	present := make(chan struct{})
	close(present)
	return &Home{
		rep:         termRep(),
		ctx:         context.Background(),
		consumed:    map[string]bool{},
		turnPending: map[string]bool{},
		ownerUp:     true,
		present:     present,
		waitChange:  make(chan struct{}),
	}
}

// TestDeliverNotice_SinkTakesIt: a registered turn sink makes the message a
// NEW TURN. This is the chain the owner-run topology's unsolicited delivery
// rides.
func TestDeliverNotice_SinkTakesIt(t *testing.T) {
	h := newNoticeHome(t)
	h.turnQ = make(chan *agentcoordpb.PeerMessage, 1)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "unsolicited"})

	select {
	case pm := <-h.turnQ:
		assert.Equal(t, "m-2", pm.GetMessageId())
	case <-time.After(time.Second):
		t.Fatal("the turn sink must take the delivery")
	}
	assert.Empty(t, h.buffer, "a turn-queued delivery is not also buffered")
}

// TestDeliverNotice_NoSinkBuffersForTheSink pins the other arm: a runner with
// no hosted engine yet (the pre-engine window) buffers the message instead of
// dropping it, for SetTurnSink to drain first.
func TestDeliverNotice_NoSinkBuffersForTheSink(t *testing.T) {
	h := newNoticeHome(t)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-4", Text: "hold this"})

	h.mu.Lock()
	defer h.mu.Unlock()
	require.Len(t, h.buffer, 1)
	assert.Equal(t, "m-4", h.buffer[0].GetMessageId())
}
