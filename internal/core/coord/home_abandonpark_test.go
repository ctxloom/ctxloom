package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// abandonPark's own comment claimed "the delivery beat us; but the
// caller is leaving — requeue", but no requeue existed: `<-p.ch` drained and
// discarded the delivered batch. deliverNotice already removed it from the
// buffer (or never buffered it — it went straight to p.ch), and
// recordReturned was never called, so the batch existed in neither the
// buffer nor `returned` — stranded. At the coordinator the ids are already
// in `delivered`, excluding them from undeliveredLocked, so re-delivery only
// ever happens if the RunChannel dies. On a LIVE channel the message was
// gone for good. Not a narrow window: Recv's own select between `<-p.ch` and
// `<-timer.C` already documents that Go picks arbitrarily when both are
// ready simultaneously.
func TestAbandonPark_RequeuesADeliveryThatWonTheRace(t *testing.T) {
	h := &Home{}
	p := &homePark{ch: make(chan []*agentcoordpb.PeerMessage, 1), done: true}
	msgs := []*agentcoordpb.PeerMessage{{MessageId: "m-1", Text: "won the race"}}
	p.ch <- msgs

	err := h.abandonPark(p, ErrRecvTimeout)
	assert.Equal(t, ErrRecvTimeout, err, "abandonPark must still return the caller's own error")

	h.mu.Lock()
	buffered := append([]*agentcoordpb.PeerMessage(nil), h.buffer...)
	h.mu.Unlock()
	require.Len(t, buffered, 1, "a delivery that won the race against the timeout/cancel must be requeued to the buffer, not dropped")
	assert.Equal(t, "m-1", buffered[0].GetMessageId())
}

// TestAbandonPark_PrependsAheadOfAnyLaterBuffered: the requeued batch arrived
// BEFORE anything a later deliverNotice call might already have appended to
// h.buffer while abandonPark's own goroutine was still unscheduled — it must
// go first, preserving delivery order.
func TestAbandonPark_PrependsAheadOfAnyLaterBuffered(t *testing.T) {
	h := &Home{}
	h.buffer = []*agentcoordpb.PeerMessage{{MessageId: "m-later", Text: "arrived after"}}
	p := &homePark{ch: make(chan []*agentcoordpb.PeerMessage, 1), done: true}
	p.ch <- []*agentcoordpb.PeerMessage{{MessageId: "m-raced", Text: "won the race"}}

	_ = h.abandonPark(p, ErrRecvTimeout)

	h.mu.Lock()
	buffered := append([]*agentcoordpb.PeerMessage(nil), h.buffer...)
	h.mu.Unlock()
	require.Len(t, buffered, 2)
	assert.Equal(t, "m-raced", buffered[0].GetMessageId(), "the raced delivery must come first")
	assert.Equal(t, "m-later", buffered[1].GetMessageId())
}

// TestAbandonPark_PreemptionSendsNilNoSpuriousRequeue: Recv's "newest
// preempts" branch sends a nil payload on the OLDER park's channel (not a
// real delivery) — abandonPark must not manufacture a phantom buffered
// message out of that.
func TestAbandonPark_PreemptionSendsNilNoSpuriousRequeue(t *testing.T) {
	h := &Home{}
	p := &homePark{ch: make(chan []*agentcoordpb.PeerMessage, 1), done: true}
	p.ch <- nil

	err := h.abandonPark(p, ErrRecvTimeout)
	assert.Equal(t, ErrRecvTimeout, err)

	h.mu.Lock()
	defer h.mu.Unlock()
	assert.Empty(t, h.buffer, "a preemption (nil payload) must not be requeued as a phantom message")
}
