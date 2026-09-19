package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// newNoticeHome builds the minimum Home deliverNotice touches: the dedupe maps,
// a context for the turn-queue send, and no pump on turnQ so the test can read
// the queue itself rather than race a sink.
func newNoticeHome(t *testing.T) *Home {
	t.Helper()
	return &Home{
		ctx:         context.Background(),
		consumed:    map[string]bool{},
		turnPending: map[string]bool{},
	}
}

// TestDeliverNotice_LiveParkWinsOverTheTurnSink pins the modality race at the
// runner's delivery-by-state seam: when a recv is parked AND a hosted engine has
// registered a turn sink, the PARK wins. It has to: the harness is actively
// polling for that message, and delivering it as an unrequested turn instead
// would leave the poll hanging on mail that was already spent.
func TestDeliverNotice_LiveParkWinsOverTheTurnSink(t *testing.T) {
	h := newNoticeHome(t)
	h.turnQ = make(chan *agentcoordpb.PeerMessage, 1)
	park := &homePark{ch: make(chan []*agentcoordpb.PeerMessage, 1)}
	h.park = park
	h.parked = true

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "for the parked recv"})

	select {
	case msgs := <-park.ch:
		require.Len(t, msgs, 1)
		assert.Equal(t, "m-1", msgs[0].GetMessageId())
	case <-time.After(time.Second):
		t.Fatal("a live parked recv must be completed by the delivery")
	}
	assert.Empty(t, h.turnQ, "the message must not ALSO be queued as an unrequested turn")
	assert.False(t, h.turnPending["m-1"], "a park-completed delivery is not a pending turn")
}

// TestDeliverNotice_SinkTakesItWhenNoParkIsLive is the other half of the same
// branch: with no park (or a park already completed), a registered turn sink
// makes the message a NEW TURN. This is the chain the owner-run topology's
// unsolicited delivery rides.
func TestDeliverNotice_SinkTakesItWhenNoParkIsLive(t *testing.T) {
	h := newNoticeHome(t)
	h.turnQ = make(chan *agentcoordpb.PeerMessage, 1)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "unsolicited"})

	select {
	case pm := <-h.turnQ:
		assert.Equal(t, "m-2", pm.GetMessageId())
	case <-time.After(time.Second):
		t.Fatal("with no live park the turn sink must take the delivery")
	}
	assert.Empty(t, h.buffer, "a turn-queued delivery is not also buffered for a later recv")

	// A park that already COMPLETED is not a live park: the sink takes it too.
	h.park = &homePark{ch: make(chan []*agentcoordpb.PeerMessage, 1), done: true}
	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-3", Text: "after the park completed"})
	select {
	case pm := <-h.turnQ:
		assert.Equal(t, "m-3", pm.GetMessageId())
	case <-time.After(time.Second):
		t.Fatal("a done park must not hold the delivery back from the sink")
	}
}

// TestDeliverNotice_NoSinkBuffersForALaterRecv pins the third arm: a runner with
// neither a park nor a hosted engine (the pre-engine window, or a shim-only
// child) buffers the message instead of dropping it.
func TestDeliverNotice_NoSinkBuffersForALaterRecv(t *testing.T) {
	h := newNoticeHome(t)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-4", Text: "hold this"})

	h.mu.Lock()
	defer h.mu.Unlock()
	require.Len(t, h.buffer, 1)
	assert.Equal(t, "m-4", h.buffer[0].GetMessageId())
}
