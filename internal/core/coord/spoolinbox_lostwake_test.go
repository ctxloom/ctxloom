package coord

import (
	"context"
	"os"
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
		func(role, _ string) { writeSpoolMail(t, role, "child-harp-1", KindMessage, "landed as it parked") },
		func(string, string) {}, alwaysLive)

	start := time.Now()
	msgs, err := in.recv(context.Background(), harp, "", 5*time.Second)
	require.NoError(t, err, "the mail was on disk the whole time the receive was parked")
	require.Len(t, msgs, 1)
	assert.Equal(t, "landed as it parked", msgs[0].Body)
	assert.Less(t, time.Since(start), 5*time.Second, "received without waiting out the park")
	assert.False(t, in.parked(harp), "a receive that returned mail leaves no poll behind")
}

// alwaysLive is the liveness check of an inbox whose every run is live.
func alwaysLive(string, string) bool { return true }

// mustRegister parks a poll for a live run.
func mustRegister(t *testing.T, in *spoolInbox, role string) *parkedPoll {
	t.Helper()
	p, err := in.register(role, "")
	require.NoError(t, err)
	return p
}

// newBareInbox is an owner inbox with no park accounting, for tests that
// drive its steps one at a time.
func newBareInbox() *spoolInbox {
	return newSpoolInbox(report.To(termSink()), spool.NewHomeMapper(), &SpoolDeliveryCounters{}, func(string, string) {}, func(string, string) {}, alwaysLive)
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

	pA := mustRegister(t, in, harp)
	in.ack(harp) // B's ack: nothing handed yet
	_, _, bFound := in.claim(harp)
	require.False(t, bFound, "B's claim finds nothing, so B goes on to park")
	writeSpoolMail(t, harp, "child-harp-1", KindMessage, "X")
	msgs, names, ok := in.claim(harp) // A's post-park claim
	require.True(t, ok)
	pB := mustRegister(t, in, harp) // B preempts A

	got, _, err := in.retire(harp, pA, msgs, names)
	require.ErrorIs(t, err, ErrRecvPreempted, "a superseded receive answers as preempted")
	assert.Nil(t, got)
	in.mu.Lock()
	assert.Empty(t, in.handed[harp], "X is not on the cursor, so no ack can consume it unseen")
	in.mu.Unlock()

	_, _, err = in.abandon(harp, pB, ErrRecvTimeout, false) // B's wait ends
	require.ErrorIs(t, err, ErrRecvTimeout)
	next, err := in.recv(context.Background(), harp, "", 0)
	require.NoError(t, err)
	require.Len(t, next, 1, "the next receive delivers X")
	assert.Equal(t, "X", next[0].Body)
}

// TestSpoolInboxAck_NewerReceiveCannotConsumeAnOlderReceivesInFlightMail
// forces overlapping receives: A's post-park claim takes X, and before A
// returns, a newer receive B runs its ack. X was handed to A, not returned by
// it, so B's ack must leave it in in/claimed/ — consuming it would lose it the
// moment A is preempted. A then answers as preempted, and X reaches the next
// receive.
func TestSpoolInboxAck_NewerReceiveCannotConsumeAnOlderReceivesInFlightMail(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-overlap"
	in := newBareInbox()

	pA := mustRegister(t, in, harp)
	writeSpoolMail(t, harp, "child-harp-1", KindMessage, "X")
	msgs, names, ok := in.claim(harp) // A's post-park claim
	require.True(t, ok)
	in.ack(harp) // B's ack, while A is still in flight
	assert.Equal(t, names, claimedNames(t, harp), "B's ack left A's in-flight X claimed, not consumed")

	pB := mustRegister(t, in, harp) // B preempts A
	got, _, err := in.retire(harp, pA, msgs, names)
	require.ErrorIs(t, err, ErrRecvPreempted)
	assert.Nil(t, got)
	_, _, err = in.abandon(harp, pB, ErrRecvTimeout, false)
	require.ErrorIs(t, err, ErrRecvTimeout)

	next, err := in.recv(context.Background(), harp, "", 0)
	require.NoError(t, err)
	require.Len(t, next, 1, "the next receive delivers X")
	assert.Equal(t, "X", next[0].Body)
}

// claimedNames lists role's in/claimed/ — mail taken by a receive and not
// yet acknowledged.
func claimedNames(t *testing.T, role string) []string {
	t.Helper()
	dir, err := spool.DirPath(spool.NewHomeMapper(), role, spool.ClaimedDirName)
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
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
		func(role, _ string) {
			writeSpoolMail(t, role, "child-harp-1", KindMessage, "for a caller who left")
			cancel()
		},
		func(string, string) {}, alwaysLive)

	msgs, err := in.recv(ctx, harp, "", 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, msgs)
	in.mu.Lock()
	assert.Empty(t, in.handed[harp], "nothing was handed to the caller that left")
	in.mu.Unlock()
	assert.False(t, in.parked(harp), "the cancelled receive leaves no poll behind")

	next, err := in.recv(context.Background(), harp, "", 0)
	require.NoError(t, err)
	require.Len(t, next, 1, "the next receive delivers it")
	assert.Equal(t, "for a caller who left", next[0].Body)
}
