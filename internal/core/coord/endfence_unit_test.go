package coord

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// notToldWarning is the start of fenceRunner's warning about a runner that
// was not told its run is ending.
const notToldWarning = "was not told its run is ending"

// An ending mark is counted: two terminals of one run may race, and the first
// to finish must not lift the other's mark. The last unmark forgets the run.
func TestEndFence_EndingMarksAreCounted(t *testing.T) {
	c := &Coordinator{ending: map[string]int{}}
	c.markEnding("run-a")
	c.markEnding("run-a")
	c.unmarkEnding("run-a")
	assert.True(t, c.isEnding("run-a"), "one of two terminals finished; the other's mark stands")
	c.unmarkEnding("run-a")
	assert.False(t, c.isEnding("run-a"), "both terminals finished")
	assert.NotContains(t, c.ending, "run-a", "a run no terminal is ending is not kept")
}

// Deregistering one of a run's waiting fences leaves the others where
// releaseFences finds them; deregistering the last forgets the run.
func TestEndFence_DeregisteringAFenceSparesTheOthers(t *testing.T) {
	c := &Coordinator{fences: map[string][]*context.CancelCauseFunc{}}
	first, releaseFirst := context.WithCancelCause(context.Background())
	second, releaseSecond := context.WithCancelCause(context.Background())
	dropFirst := c.trackFence("run-a", &releaseFirst)
	dropSecond := c.trackFence("run-a", &releaseSecond)

	dropFirst()
	require.Error(t, first.Err(), "a deregistered fence is released")
	c.releaseFences("run-a")
	assert.ErrorIs(t, context.Cause(second), errEndRecorded, "the fence still waiting is released by the recorded end")

	dropSecond()
	assert.NotContains(t, c.fences, "run-a", "a run with no waiting fence is not kept")
}

// liveFence is a live, idle child whose runner answers the end fence's
// PauseRun as refuse decides, on a coordinator whose findings the test reads.
type liveFence struct {
	c        *Coordinator
	findings *report.Collector
	runID    string
	pauses   atomic.Int32
}

func newLiveFence(t *testing.T, refuse error) *liveFence {
	t.Helper()
	resetStrictness(t)
	f := &liveFence{findings: &report.Collector{}}
	sp := startRunSpawner(t, nil)
	var target atomic.Value
	target.Store("")
	sp.refuse = func(req *agentcoordpb.RunnerRequest) error {
		if p := req.GetPauseRun(); p != nil && p.GetRunId() == target.Load().(string) {
			f.pauses.Add(1)
			return refuse
		}
		return nil
	}
	f.c = newTestCoordinatorWith(t, sp, func(o *Options) { o.Reporter = f.findings })
	out, err := f.c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(f.c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	require.True(t, f.c.runnerConnected(out.RunID), "premise: the run's runner is connected")
	f.runID = out.RunID
	target.Store(out.RunID)
	return f
}

// notTold counts the warnings that a runner was not told its run is ending.
func (f *liveFence) notTold() int {
	n := 0
	for _, text := range f.findings.All().Texts() {
		if strings.Contains(text, notToldWarning) {
			n++
		}
	}
	return n
}

// A live run's runner that takes the pause is fenced without a word.
func TestEndFence_ARunnerThatTakesThePauseIsFencedSilently(t *testing.T) {
	f := newLiveFence(t, nil)
	rec := f.c.fenceRunner(f.runID, CauseStopped)
	require.NotNil(t, rec, "a live run on a connected runner is fenced")
	assert.Equal(t, f.runID, rec.RunID)
	assert.EqualValues(t, 1, f.pauses.Load(), "the runner was told its run is ending")
	assert.Zero(t, f.notTold(), "a fence the runner took is not warned about")
}

// A runner that refuses the pause is warned about, and the end goes ahead.
func TestEndFence_ARunnerThatRefusesThePauseIsWarnedAbout(t *testing.T) {
	f := newLiveFence(t, errors.New("not now"))
	rec := f.c.fenceRunner(f.runID, CauseStopped)
	require.NotNil(t, rec, "the end goes ahead with the run it tried to fence")
	assert.EqualValues(t, 1, f.pauses.Load(), "premise: the pause reached the runner")
	assert.Equal(t, 1, f.notTold(), "a refused fence is warned about")
}

// A run whose runner is not connected has nothing to fence: no pause is
// attempted, so none is warned about.
func TestEndFence_ARunWithNoRunnerConnectedIsNotFenced(t *testing.T) {
	f := newLiveFence(t, nil)
	var credHash string
	f.c.runs.View(func() { credHash = f.c.runsF.run(f.runID).CredHash })
	f.c.mu.Lock()
	session := f.c.runners[credHash]
	delete(f.c.runners, credHash)
	f.c.mu.Unlock()
	t.Cleanup(func() {
		f.c.mu.Lock()
		f.c.runners[credHash] = session
		f.c.mu.Unlock()
	})
	require.False(t, f.c.runnerConnected(f.runID), "premise: the runner reads disconnected")

	assert.Nil(t, f.c.fenceRunner(f.runID, CauseStopped), "nothing was fenced")
	assert.Zero(t, f.pauses.Load(), "no pause was sent")
	assert.Zero(t, f.notTold(), "nothing was attempted, so nothing is warned about")
}

// A run whose end is already journaled has nothing left to fence, though its
// runner is still connected and its teardown still to come.
func TestEndFence_AnEndedRunIsNotFencedAgain(t *testing.T) {
	f := newEndFenceFixture(t)
	f.endAndHold(t)
	require.True(t, f.c.runnerConnected(f.runID), "premise: the ended run's runner is still connected")
	f.paused.Store(false)

	assert.Nil(t, f.c.fenceRunner(f.runID, CauseStopped), "an ended run is not fenced")
	assert.False(t, f.paused.Load(), "no second pause was sent")
}
