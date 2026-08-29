package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// courierUnderTest builds a courier over a real spool on a temp home, with a
// ring that RECORDS instead of sending. The recording is the point: the
// guarantee under test is that a write is accompanied by a ring, and the only
// way to assert that is to watch the ring.
func courierUnderTest(t *testing.T) (*spoolCourier, *[]spool.Ref) {
	t.Helper()
	testsupport.Isolate(t)
	var rung []spool.Ref
	return &spoolCourier{
		writers: newSpoolWriterCache(spool.NewHomeMapper(), spool.DirIn, "test"),
		keyFor:  func(to string) string { return to },
		ring: func(_ string, ref spool.Ref) error {
			rung = append(rung, ref)
			return nil
		},
		side: "test",
	}, &rung
}

// THE GUARANTEE THE CONSTRUCT EXISTS FOR. Six sites used to write a spool file
// and then SEPARATELY remember to ring, so "wrote the file and notified nobody"
// was expressible at every one of them — which is the shape of the delivery
// failures that cost this project a long investigation. The pairing is now one
// operation, and this is the assertion that it stays one.
//
// Asserts the RING FIRED for the ref that was written, not merely that Send
// returned without error: a Send that wrote the file and rang nothing would
// return a perfectly good ref.
func TestSpoolCourier_SendAlwaysRings(t *testing.T) {
	x, rung := courierUnderTest(t)

	ref, err := x.Send(Message{
		ID: "m1", From: "child", To: "amber-quiet-heron", Kind: KindResult, Body: "the finding",
	})
	require.NoError(t, err)

	require.Len(t, *rung, 1, "a write that rang nothing is the defect this construct removes")
	assert.Equal(t, ref, (*rung)[0], "the doorbell must name the ref that was just written")
}

// SendProjected is the layer the shadow tee composes on, and it must carry the
// same guarantee — otherwise a caller could reach past Send to write without
// ringing, which is exactly the escape hatch this design closes.
func TestSpoolCourier_SendProjectedAlsoRings(t *testing.T) {
	x, rung := courierUnderTest(t)

	sm, err := spoolMessageForMail(Message{
		ID: "m2", From: "child", To: "amber-quiet-heron", Kind: KindResult, Body: "projected",
	}, "amber-quiet-heron")
	require.NoError(t, err)

	ref, err := x.SendProjected("amber-quiet-heron", sm)
	require.NoError(t, err)

	require.Len(t, *rung, 1, "SendProjected must ring too, or it is a way to write without announcing")
	assert.Equal(t, ref, (*rung)[0])
}

// Announce covers the OTHER mutation: a rename already committed to disk (the
// consume-rename, a withdrawal). The courier owns the general invariant — a
// spool mutation is announced — not merely the write case.
func TestSpoolCourier_AnnounceRingsAnAlreadyCommittedMutation(t *testing.T) {
	x, rung := courierUnderTest(t)

	ref := spool.Ref{Harp: "amber-quiet-heron", Dir: spool.DirIn, Name: "1.1.test.md"}
	x.Announce("amber-quiet-heron", ref, "consumed")

	require.Len(t, *rung, 1, "a rename nobody announced is the same silence in a different shape")
	assert.Equal(t, ref, (*rung)[0])
}

// A FAILED RING MUST NOT FAIL THE SEND. The file is durable by then and the
// recipient's sweep is the at-least-once floor, so the only cost is one sweep
// interval. Failing here would convert a latency cost into a lost message —
// the exact inversion of the contract.
func TestSpoolCourier_AFailedRingDoesNotFailTheSend(t *testing.T) {
	testsupport.Isolate(t)
	x := &spoolCourier{
		writers: newSpoolWriterCache(spool.NewHomeMapper(), spool.DirIn, "test"),
		keyFor:  func(to string) string { return to },
		ring:    func(string, spool.Ref) error { return assert.AnError },
		side:    "test",
	}

	ref, err := x.Send(Message{
		ID: "m3", From: "child", To: "amber-quiet-heron", Kind: KindResult, Body: "still delivered",
	})
	assert.NoError(t, err, "the file is on disk; a dropped doorbell costs latency, never the message")
	assert.NotEmpty(t, ref.Name)
}
