package coord

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// endFenceFixture is a live, idle child whose run's END the test can hold
// open: endRecordedHook parks the ending goroutine once the end is journaled
// and before any teardown — the span in which the roster reads ended, mail
// resumes the harp, and the ended run's runner is still alive with its run
// channel still registered.
type endFenceFixture struct {
	c         *Coordinator
	sp        *fakeSpawner
	harp      string
	runID     string
	entered   chan struct{}
	release   func()
	paused    atomic.Bool // the ended run's runner was asked to pause
	startGap  chan struct{}
	openStart func()
}

func newEndFenceFixture(t *testing.T) *endFenceFixture {
	t.Helper()
	resetStrictness(t)
	f := &endFenceFixture{sp: startRunSpawner(t, nil), entered: make(chan struct{}), startGap: make(chan struct{})}
	var target atomic.Value
	target.Store("")
	hold := make(chan struct{})
	var enterOnce, releaseOnce, startOnce sync.Once
	f.release = func() { releaseOnce.Do(func() { close(hold) }) }
	f.sp.refuse = func(req *agentcoordpb.RunnerRequest) error {
		if p := req.GetPauseRun(); p != nil && p.GetRunId() == target.Load().(string) {
			f.paused.Store(true)
		}
		return nil
	}
	f.c = newTestCoordinatorWith(t, f.sp, func(o *Options) {
		o.endRecordedHook = func(runID string) {
			if runID == target.Load().(string) {
				enterOnce.Do(func() { close(f.entered) })
				<-hold
			}
		}
	})
	// Registered after the coordinator's own cleanup, so they run before its
	// Close: a held end or launch would otherwise stall it.
	t.Cleanup(func() { startOnce.Do(func() { close(f.startGap) }) })
	t.Cleanup(f.release)
	f.openStart = func() { startOnce.Do(func() { close(f.startGap) }) }
	out, err := f.c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(f.c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	f.harp, f.runID = out.Harp, out.RunID
	target.Store(out.RunID)
	// The resumed run's launch is held until the ended run's teardown has
	// run, so which runner sweeps the follow-up first is the test's choice.
	f.sp.mu.Lock()
	f.sp.startGate = f.startGap
	f.sp.mu.Unlock()
	return f
}

// endAndHold ends the idle child by its FINAL (between turns, so at once) and
// waits until the end is journaled with its teardown still to come.
func (f *endFenceFixture) endAndHold(t *testing.T) {
	t.Helper()
	require.NoError(t, f.c.recordSummary(f.harp, f.runID, 1, finalSummary("FINAL: done")))
	within(t, f.entered, "the run's end was never journaled")
	require.Equal(t, StateEnded, rosterState(f.c, f.harp), "premise: the roster reads the run ended")
}

// TestEndFence_MailForAnEndedRunWaitsForTheResumedRun forces the swallow: the
// run's end is journaled, so a follow-up resumes the harp as a fresh run —
// but the ended run's runner is still alive (its teardown is still to come)
// and its own in/ sweep, rung by a doorbell or its timer, finds the follow-up
// first. Delivered there, its idle engine takes the turn and the file is
// consumed: the resumed run, the one the follow-up was for, never sees it.
//
// The sweep is forced (SweepSpoolIn is the timer's own call), and the test
// waits until the ended runner has either consumed the mail or been told its
// run is over — so the resumed run's launch, held until then, can only find
// what that runner left behind.
func TestEndFence_MailForAnEndedRunWaitsForTheResumedRun(t *testing.T) {
	f := newEndFenceFixture(t)
	f.endAndHold(t)

	_, err := f.c.AgentSend(ownerIdentity(), f.harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	ended := f.sp.engineHome(0)
	ended.SweepSpoolIn()
	require.Eventually(t, func() bool { return ended.SpoolDeliveryStats().Delivered >= 1 }, conformanceWait, time.Millisecond,
		"premise: the ended run's still-live runner swept the follow-up")
	require.Eventually(t, func() bool { return ended.SpoolDeliveryStats().Consumed >= 1 || f.paused.Load() }, conformanceWait, time.Millisecond,
		"the ended run's runner neither took the follow-up nor was told its run is over")

	f.release()
	f.openStart()
	awaitChatText(t, f.sp, 1, "one more thing")
	assert.Zero(t, countChatText(f.sp, 0, "one more thing"), "the ENDED run's engine took mail meant for the resumed run")
	assert.NotEqual(t, f.runID, currentRunID(f.c, f.harp))
}

// TestEndFence_NoDoorbellRingsAnEndedRunsChannel is the defence in depth:
// while an ended run's channel is still registered (its teardown has not
// reached it), a doorbell for the harp's spool is not rung on it — the
// ended run's runner is not the reader that mail is for. The file is the
// truth, so the drop costs nothing: the resumed run's runner sweeps on
// attach.
func TestEndFence_NoDoorbellRingsAnEndedRunsChannel(t *testing.T) {
	f := newEndFenceFixture(t)
	f.endAndHold(t)
	f.c.mu.Lock()
	ch := f.c.chans[f.harp]
	f.c.mu.Unlock()
	require.NotNil(t, ch, "premise: the ended run's channel is still registered")
	require.Equal(t, f.runID, ch.id.RunID, "premise: the registered channel is the ended run's")

	before := f.c.SpoolDoorbellStats().Dropped
	require.NoError(t, f.c.ringSpool(f.harp, spool.Ref{Harp: f.harp, Dir: spool.DirIn, Name: "1.1.test.md"}))
	assert.Equal(t, before+1, f.c.SpoolDoorbellStats().Dropped, "a doorbell rang on an ended run's channel")
}

// TestEndFence_AnEndingRunIsSentNoResume: the fence is a pause, and a pause is
// lifted by any ResumeRun — a hold's release among them. While the run is
// ending none is sent, so the pause its end set stays final.
func TestEndFence_AnEndingRunIsSentNoResume(t *testing.T) {
	f := newEndFenceFixture(t)
	f.endAndHold(t)
	require.True(t, f.paused.Load(), "premise: the ending run's runner was told its run is over")
	rec := f.c.currentRunRecord(f.harp)
	require.NotNil(t, rec)
	require.Equal(t, f.runID, rec.RunID, "premise: the harp's current run is still the ending one")

	_, err := f.c.sendRunnerControl(context.Background(), rec, "resume", "")
	require.ErrorIs(t, err, ErrCapabilityUnavailable, "a resume reached an ending run's runner")
}

// TestEndRun_SessionEndIsScopedToTheEndedRun: the run's end names the RUN to
// the spawner's session accounting, so the spawner can tell a late end of an
// ended run from the end of the resumed run that now holds the session
// (spawn's TestProdSpawner_ALateEndOfAnEndedRunSparesItsResumedSuccessor).
func TestEndRun_SessionEndIsScopedToTheEndedRun(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(t, nil)
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	require.NoError(t, c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: done")))
	require.Eventually(t, func() bool { return len(sp.sessionEndRunIDs()) > 0 }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, []string{out.RunID}, sp.sessionEndRunIDs())
}
