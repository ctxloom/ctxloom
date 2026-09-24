package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// TestSpoolInboxRecv_MailLandingAsItParksIsReceived pins the park race: the
// courier rings only after the file is on disk, and a wake finding no parked
// poll is dropped. Mail that lands after a receive's first claim but before
// its poll is registered therefore rings nobody — the receive must still
// find it rather than sit out its whole wait beside it. onPark runs inside
// exactly that window (registered, not yet blocked), so the file written
// there with no wake at all is the lost-wake interleaving, made deterministic.
func TestSpoolInboxRecv_MailLandingAsItParksIsReceived(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-lostwake"
	in := newSpoolInbox(report.To(termSink()), spool.NewHomeMapper(), &SpoolDeliveryCounters{},
		func(role string) { writeSpoolMail(t, role, "child-harp-1", KindMessage, "landed as it parked") },
		func(string) {})

	start := time.Now()
	msgs, err := in.recv(context.Background(), harp, 5*time.Second)
	require.NoError(t, err, "the mail was on disk the whole time the receive was parked")
	require.Len(t, msgs, 1)
	assert.Equal(t, "landed as it parked", msgs[0].Body)
	assert.Less(t, time.Since(start), 5*time.Second, "received without waiting out the park")
	assert.False(t, in.parked(harp), "a receive that returned mail leaves no poll behind")
}

// newBareInbox is an owner inbox with no park accounting, for tests that
// drive its steps one at a time.
func newBareInbox() *spoolInbox {
	return newSpoolInbox(report.To(termSink()), spool.NewHomeMapper(), &SpoolDeliveryCounters{}, func(string) {}, func(string) {})
}

// TestSpoolInboxRetire_PreemptedAfterItsClaimLeavesTheMailForTheNextReceive
// forces: receive A is parked; a newer receive B has acked and found
// nothing; A's post-park claim then takes X; only then does B register and
// preempt A. A is superseded — a preempted receive answers empty — so X must
// come off A's ack cursor and stay claimed for the next receive, not ride
// out to A and be consumed by the next ack unseen.
func TestSpoolInboxRetire_PreemptedAfterItsClaimLeavesTheMailForTheNextReceive(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-preempt-b"
	in := newBareInbox()

	pA := in.register(harp)
	in.ack(harp) // B's ack: nothing handed yet
	_, _, bFound := in.claim(harp)
	require.False(t, bFound, "B's claim finds nothing, so B goes on to park")
	writeSpoolMail(t, harp, "child-harp-1", KindMessage, "X")
	msgs, names, ok := in.claim(harp) // A's post-park claim
	require.True(t, ok)
	pB := in.register(harp) // B preempts A

	got, err := in.retire(harp, pA, msgs, names)
	require.ErrorIs(t, err, ErrRecvPreempted, "a superseded receive answers as preempted")
	assert.Nil(t, got)
	in.mu.Lock()
	assert.Empty(t, in.handed[harp], "X is off the cursor, so no ack can consume it unseen")
	in.mu.Unlock()

	_, err = in.abandon(harp, pB, ErrRecvTimeout, false) // B's wait ends
	require.ErrorIs(t, err, ErrRecvTimeout)
	next, err := in.recv(context.Background(), harp, 0)
	require.NoError(t, err)
	require.Len(t, next, 1, "the next receive delivers X")
	assert.Equal(t, "X", next[0].Body)
}

// TestSpoolInboxRetire_PreemptedAfterANewerAckConsumedItsClaimStillDelivers
// forces the other order: A's post-park claim takes X; B's ack then consumes
// X (the cursor is acked wholesale) and B preempts A. X is in in/consumed/
// and has been seen by nobody, so answering A as preempted would lose it; A's
// caller is still waiting on this very call and is the only one left who can
// be given it.
func TestSpoolInboxRetire_PreemptedAfterANewerAckConsumedItsClaimStillDelivers(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-preempt-a"
	in := newBareInbox()

	pA := in.register(harp)
	writeSpoolMail(t, harp, "child-harp-1", KindMessage, "X")
	msgs, names, ok := in.claim(harp) // A's post-park claim
	require.True(t, ok)
	in.ack(harp) // B's ack consumes X
	_, _, bFound := in.claim(harp)
	require.False(t, bFound)
	pB := in.register(harp) // B preempts A
	t.Cleanup(func() { _, _ = in.abandon(harp, pB, ErrRecvTimeout, false) })

	got, err := in.retire(harp, pA, msgs, names)
	require.NoError(t, err, "X is already consumed: a preempted answer here would lose it")
	require.Len(t, got, 1)
	assert.Equal(t, "X", got[0].Body)
}

// TestSpoolInboxRecv_CancelledWhileParkingClaimsNothing forces a context
// cancelled inside the park window, with mail already on disk: the caller has
// gone, so the post-park claim must not hand the mail to it — that would put
// it on the ack cursor for a receive nobody reads, and the next ack would
// consume it unseen. The mail stays for the next receive.
func TestSpoolInboxRecv_CancelledWhileParkingClaimsNothing(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-cancelled"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newSpoolInbox(report.To(termSink()), spool.NewHomeMapper(), &SpoolDeliveryCounters{},
		func(role string) {
			writeSpoolMail(t, role, "child-harp-1", KindMessage, "for a caller who left")
			cancel()
		},
		func(string) {})

	msgs, err := in.recv(ctx, harp, 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, msgs)
	in.mu.Lock()
	assert.Empty(t, in.handed[harp], "nothing was handed to the caller that left")
	in.mu.Unlock()
	assert.False(t, in.parked(harp), "the cancelled receive leaves no poll behind")

	next, err := in.recv(context.Background(), harp, 0)
	require.NoError(t, err)
	require.Len(t, next, 1, "the next receive delivers it")
	assert.Equal(t, "for a caller who left", next[0].Body)
}
