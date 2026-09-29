package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
)

// TestHome_OutboundWrittenInTheAttachWindowIsNotStrandedUntilTheSlowSweep
// forces the RunChannel handshake's window by hand: the coordinator attaches
// (and runs its per-child attach sweep) when it READS the Hello, the runner
// adopts the stream only after it reads the HelloAck. An out/ write made
// between the two rings into a nil stream — dropped by design — and the
// coordinator's attach sweep has already looked. Adopting sends the runner's
// attach Heartbeat, and the coordinator re-sweeps out/ on that first frame
// (Coordinator.ConfirmAttach), so the file must leave out/ well inside the
// slow timer (conformanceWait, not spoolSweepInterval).
func TestHome_OutboundWrittenInTheAttachWindowIsNotStrandedUntilTheSlowSweep(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, nil, nil)
	harp := coordharness.OwnerHarp // the declared owner: a sender identity with no run record

	url, err := c.ReachURL("host")
	require.NoError(t, err)
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      url, Token: token, Harness: "test", Version: "test",
		Harp: harp,
		// The Home's own loop must not redial into the window this test holds open.
		RedialBackoff: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	require.Eventually(t, h.Attached, conformanceWait, 10*time.Millisecond)

	// A sentinel no doorbell announces: only the attach sweep forced below can
	// move it, so its departure proves that sweep has run.
	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirOut, harp)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{Kind: coord.KindMessage, FromHarp: harp, To: harp, Body: "sentinel"})
	require.NoError(t, err)

	// Coordinator half of a reconnect: Hello read, channel attached (newest
	// wins, so the loop's stream is cancelled), attach sweep marked.
	stream, err := h.openRunChannel(agentcoordpb.NewCoordinatorServiceClient(h.conn))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(spoolEntries(t, harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond,
		"the coordinator's attach sweep must have run before the window is entered")
	require.Eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.stream == nil }, conformanceWait, 10*time.Millisecond,
		"the superseded stream must be detached on the runner's side")

	// THE WINDOW: the runner writes its turn's result while it holds no stream.
	dropped := h.SpoolDoorbellStats().Dropped
	_, err = h.writeOutbound(coord.Message{From: harp, To: harp, Kind: coord.KindMessage, Body: "written in the window"})
	require.NoError(t, err)
	require.Equal(t, dropped+1, h.SpoolDoorbellStats().Dropped, "the doorbell rang into a nil stream")

	// Runner half: adopt the stream, exactly as runChannelOnce does.
	h.attachAndReissue(stream)
	go func() {
		for {
			f, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			h.handleCoordinatorFrame(f)
		}
	}()

	require.Eventually(t, func() bool { return len(spoolEntries(t, harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond,
		"an out/ file whose doorbell was dropped in the attach window must be swept once the runner attaches, not left for the slow timer")
}
