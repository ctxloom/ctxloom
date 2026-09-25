package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// recvHome is a Home whose coordinator never answers, so every event it
// emits stays in unacked where a test can read it.
func recvHome(t *testing.T) *Home {
	t.Helper()
	return ownerLossHome(t, "http://127.0.0.1:1/mcp", time.Hour)
}

// customEvents lists the names of the custom events h has emitted.
func customEvents(h *Home) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, ev := range h.unacked {
		if c := ev.GetCustom(); c != nil {
			out = append(out, c.GetName())
		}
	}
	return out
}

func isParked(h *Home) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.park != nil && !h.park.done
}

type recvResult struct {
	msgs []*agentcoordpb.PeerMessage
	err  error
}

func recvAsync(h *Home, ctx context.Context, wait time.Duration) <-chan recvResult {
	out := make(chan recvResult, 1)
	go func() {
		msgs, err := h.Recv(ctx, wait)
		out <- recvResult{msgs, err}
	}()
	return out
}

func awaitRecv(t *testing.T, ch <-chan recvResult) recvResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(conformanceWait):
		t.Fatal("Recv never returned")
		return recvResult{}
	}
}

// TestHomeRecv_BufferedReturnsAtOnceAndAcksOnTheNextCall: a buffered notice
// returns without parking, and the NEXT Recv acknowledges it (cursor-ack) —
// here an ack with no spool file behind it, counted as a failed delivery.
func TestHomeRecv_BufferedReturnsAtOnceAndAcksOnTheNextCall(t *testing.T) {
	h := recvHome(t)
	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "hi"})

	msgs, err := h.Recv(context.Background(), time.Hour)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "m-1", msgs[0].GetMessageId())
	require.Empty(t, customEvents(h), "a buffered hit never parks")
	require.Zero(t, h.spoolDeliveryCount.Failed.Load())

	_, err = h.Recv(context.Background(), time.Millisecond)
	require.ErrorIs(t, err, coord.ErrRecvTimeout)
	require.Equal(t, uint64(1), h.spoolDeliveryCount.Failed.Load(), "the second Recv acks the first one's batch")

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "again"})
	h.mu.Lock()
	require.Empty(t, h.buffer, "a consumed id is never delivered again")
	h.mu.Unlock()
}

// TestHomeRecv_ParkedDeliveryUnparks: an empty buffer parks (one parked
// event), a delivery completes the park, and the park is released (one
// unparked event).
func TestHomeRecv_ParkedDeliveryUnparks(t *testing.T) {
	h := recvHome(t)
	res := recvAsync(h, context.Background(), time.Hour)
	require.Eventually(t, func() bool { return isParked(h) }, conformanceWait, time.Millisecond)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "hello"})
	r := awaitRecv(t, res)
	require.NoError(t, r.err)
	require.Len(t, r.msgs, 1)
	require.Equal(t, "m-2", r.msgs[0].GetMessageId())
	require.Equal(t, []string{coord.CustomRecvParked, coord.CustomRecvUnparked}, customEvents(h))
	h.mu.Lock()
	require.False(t, h.parked)
	h.mu.Unlock()
}

// TestHomeRecv_NewerPreemptsOlder: a second Recv preempts the parked one,
// which returns ErrRecvPreempted without emitting an unpark; the park stays
// held, so no second parked event is emitted either.
func TestHomeRecv_NewerPreemptsOlder(t *testing.T) {
	h := recvHome(t)
	older := recvAsync(h, context.Background(), time.Hour)
	require.Eventually(t, func() bool { return isParked(h) }, conformanceWait, time.Millisecond)

	newer := recvAsync(h, context.Background(), 50*time.Millisecond)
	r := awaitRecv(t, older)
	require.ErrorIs(t, r.err, coord.ErrRecvPreempted)
	require.Nil(t, r.msgs)

	r = awaitRecv(t, newer)
	require.ErrorIs(t, r.err, coord.ErrRecvTimeout)
	require.Equal(t, []string{coord.CustomRecvParked, coord.CustomRecvUnparked}, customEvents(h))
}

// TestHomeRecv_AbandonReasons: a park ends with the timeout, the caller's
// context error, or ErrCoordinatorUnreachable once the Home is torn down —
// each releasing the park.
func TestHomeRecv_AbandonReasons(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		h := recvHome(t)
		_, err := h.Recv(context.Background(), time.Millisecond)
		require.ErrorIs(t, err, coord.ErrRecvTimeout)
		require.Equal(t, []string{coord.CustomRecvParked, coord.CustomRecvUnparked}, customEvents(h))
		require.False(t, isParked(h))
	})
	t.Run("caller context", func(t *testing.T) {
		h := recvHome(t)
		ctx, cancel := context.WithCancel(context.Background())
		res := recvAsync(h, ctx, time.Hour)
		require.Eventually(t, func() bool { return isParked(h) }, conformanceWait, time.Millisecond)
		cancel()
		r := awaitRecv(t, res)
		require.ErrorIs(t, r.err, context.Canceled)
		require.False(t, isParked(h))
	})
	t.Run("home torn down", func(t *testing.T) {
		h := recvHome(t)
		res := recvAsync(h, context.Background(), time.Hour)
		require.Eventually(t, func() bool { return isParked(h) }, conformanceWait, time.Millisecond)
		h.cancel()
		r := awaitRecv(t, res)
		require.ErrorIs(t, r.err, ErrCoordinatorUnreachable)
		require.False(t, isParked(h))
	})
}
