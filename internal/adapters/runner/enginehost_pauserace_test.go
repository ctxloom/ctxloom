package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestEnqueueTurn_APauseLandingWhileItWaitsForTheTurnInFlightHoldsTheNextTurn
// FORCES the interleaving enqueueTurn's two gates leave open: a caller passes
// the pause gate (unpaused), queues behind the turn in flight, and a pause
// lands while it waits there. When the turn in flight reaches its boundary the
// queued turn must NOT start — the run is paused. It is the same window a held
// turn failure's self-park lands in (installPause runs before the failing
// turn's boundary), where a missed gate would put the next turn on the spent
// credential and consume its mail for nothing.
//
// The enqueueAt seam is set before the run starts; the briefing is held in
// flight at the scripted engine's gate, so the interleaving is placed, never
// waited for.
func TestEnqueueTurn_APauseLandingWhileItWaitsForTheTurnInFlightHoldsTheNextTurn(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	sc := &scriptedChat{Gate: release, Entered: entered}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	points := make(chan enqueuePoint, 16)
	eh.enqueueAt = func(at enqueuePoint) { points <- at }
	t.Cleanup(eh.Close)
	eh.BindHome(&fakeEngineHome{})

	next := func() enqueuePoint {
		return testsupport.Within(t, 5*time.Second, func() enqueuePoint { return <-points }, "enqueueTurn reached no further point")
	}
	enteredTurn := func(what string) {
		testsupport.Within(t, 5*time.Second, func() struct{} { return <-entered }, "%s never reached the engine", what)
	}

	require.Equal(t, int32(0), handleBounded(t, eh, &agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")},
	}).GetStatus().GetCode())
	// The briefing is enqueued synchronously by StartRun and is now held in
	// flight at the engine's gate.
	require.Equal(t, enqueueAtPauseGate, next())
	require.Equal(t, enqueueAtBusy, next())
	enteredTurn("the briefing")

	queued := make(chan error, 1)
	go func() { queued <- eh.enqueueTurn(context.Background(), turnTag{}, "queued behind the briefing") }()
	require.Equal(t, enqueueAtPauseGate, next())
	require.Equal(t, enqueueAtBusy, next(), "the queued turn passed the (open) pause gate and waits on the briefing")

	// THE WINDOW: past the pause gate, waiting for the turn in flight.
	paused := eh.pauseRun(&agentcoordpb.PauseRun{RunId: "run-1"})
	require.True(t, paused.GetPauseRun().GetNewlyPaused())
	close(release) // the briefing reaches its boundary

	select {
	case <-entered:
		t.Fatal("a turn started while the run was paused: the pause landed after enqueueTurn's gate check and was missed")
	case at := <-points:
		assert.Equal(t, enqueueAtPauseGate, at, "the queued turn must go back to the pause gate")
	case <-time.After(5 * time.Second):
		t.Fatal("the queued turn neither started nor went back to the pause gate")
	}
	require.NotNil(t, eh.pauseGate())

	// The resume releases it: the held turn still starts, once.
	resumed := eh.resumeRun(&agentcoordpb.ResumeRun{RunId: "run-1"})
	require.True(t, resumed.GetResumeRun().GetNewlyResumed())
	require.Equal(t, enqueueAtBusy, next())
	require.NoError(t, testsupport.Within(t, 5*time.Second, func() error { return <-queued }, "the held turn never started"))
	enteredTurn("the held turn")
	assert.Equal(t, []string{"CTX\n\ndo the thing", "queued behind the briefing"}, sc.RecordedTexts())
}
