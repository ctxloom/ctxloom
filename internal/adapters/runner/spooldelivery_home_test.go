package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// TestSpoolDelivery_ExitedRunnerStopsSweepingIn: a runner that has reported
// RunExited has no engine to hand a turn to. If it kept sweeping in/, mail
// written for the harp's NEXT run (the resume the coordinator is about to
// launch) could be delivered into this dead sink and consumed — the next run
// then finds nothing and idles forever.
func TestSpoolDelivery_ExitedRunnerStopsSweepingIn(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const harp = "exited-runner-harp"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-exited", Harness: "mock", Harp: harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })
	delivered := make(chan string, 4)
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		delivered <- pm.GetText()
		return true
	})

	home.ReportRunExited(0, "") // no link to send on; the exit is still this runner's state

	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirIn, "coord")
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{Kind: coord.KindMessage, FromHarp: "coordinator-harp", To: harp, Body: "for the next run"})
	require.NoError(t, err)

	home.sweepSpoolIn() // the reactor's body, called directly: synchronous, so the assertion below is not a timing guess
	select {
	case text := <-delivered:
		t.Fatalf("an exited runner delivered %q into its dead engine", text)
	default:
	}
	assert.Len(t, spoolEntries(t, harp, spool.DirIn), 1, "the file stays in in/ for the run that will actually answer it")
}

// TestHome_ConsumeThatLostItsRaceIsNotAFailure pins the ENOENT contract on
// the runner's side (the coordinator's half is pinned beside its own sweep):
// a sweep that reaches a file the other path already delivered is the design
// working, not a fault — silent, and not counted as a failed delivery.
func TestHome_ConsumeThatLostItsRaceIsNotAFailure(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const harp = "raced-harp"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-raced", Harness: "mock", Harp: harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })
	mapper := spool.NewHomeMapper()

	// Runner side: a file whose consume the other path already won.
	inW, err := spool.NewWriter(mapper, harp, spool.DirIn, "coord")
	require.NoError(t, err)
	inRef, err := inW.Write(&spool.Message{Kind: coord.KindMessage, FromHarp: "coordinator-harp", To: harp, Body: "raced"})
	require.NoError(t, err)
	require.NoError(t, spool.Deliver(mapper, inRef, "m-raced", time.Now())) // the other path wins

	failedBefore := home.SpoolDeliveryStats().Failed
	home.rememberSpoolRef("m-raced", inRef)
	home.ackMailConsumed([]string{"m-raced"})
	assert.Equal(t, failedBefore, home.SpoolDeliveryStats().Failed,
		"a consume that lost its race is the other path having won, never a failed delivery")

}

// TestSpoolDelivery_ForeignHarpDoorbellIsRefused is the same discipline on the
// runner's inbound side: a runner has exactly one spool, and a doorbell naming
// any other harp is refused rather than followed.
func TestSpoolDelivery_ForeignHarpDoorbellIsRefused(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const harp = "doorbell-harp"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-doorbell", Harness: "mock", Harp: harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })

	before := home.SpoolDoorbellStats().Rejected
	home.handleSpoolChanged(&agentcoordpb.SpoolChanged{
		Harp: "somebody-elses-harp",
		Dir:  agentcoordpb.SpoolDir_SPOOL_DIR_IN,
		Name: "00000000000000000001.00000001.coord.md",
	})
	assert.Equal(t, before+1, home.SpoolDoorbellStats().Rejected,
		"a runner pointed at another session's spool must refuse, loudly and countably")
	assert.NotEqual(t, "somebody-elses-harp", harp)
}
