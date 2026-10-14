package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// A stopped run's terminal says who stopped it and why, WHICHEVER terminal
// reaches the watch stream first. The coordinator's synthesized one always
// did; the engine's own — the interrupted engine's CANCELLED result, which
// usually wins the race because the stop asks the runner first — carried no
// reason at all. On 2026-10-14 a session ended by agent_stop printed only
// "run did not succeed: RUN_STATUS_CANCELLED".

// stopCauseRig is one live child whose engine relays nothing, so the test
// may speak for its runner on the run's own (run, seq) space: the engine's
// seq is final (see TestTerminateRun_DeadEngineReasonReachesParentMailbox).
type stopCauseRig struct {
	c      *Coordinator
	sp     *fakeSpawner
	harp   string
	runID  string
	ch     *RunChannel
	seq    uint64
	events <-chan Event
}

func newStopCauseRig(t *testing.T) *stopCauseRig {
	t.Helper()
	resetStrictness(t)
	qe := newQuiescentEngine()
	sp := startRunSpawner(t, nil)
	sp.nextBackend = func() engine.Instance { return qe }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)
	select {
	case <-qe.entered:
	case <-time.After(conformanceWait):
		t.Fatal("the engine never entered its first turn")
	}
	last := sp.engineHome(0).EmittedSeq()
	var ch *RunChannel
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		ch = c.chans[out.Harp]
		return ch != nil && ch.ackSeq >= last
	}, conformanceWait, 5*time.Millisecond)
	_, events, cancel, _ := c.WatchRuns([]string{out.RunID})
	t.Cleanup(cancel)
	return &stopCauseRig{c: c, sp: sp, harp: out.Harp, runID: out.RunID, ch: ch, seq: last, events: events}
}

// terminal waits for the run's one RunCompleted on the watch stream.
func (r *stopCauseRig) terminal(t *testing.T) Result {
	t.Helper()
	deadline := time.After(conformanceWait)
	for {
		select {
		case ev := <-r.events:
			if rc, ok := ev.Payload.(RunCompleted); ok {
				require.NotNil(t, rc.Result)
				return *rc.Result
			}
		case <-deadline:
			t.Fatal("the run's RunCompleted never reached the watch stream")
		}
	}
}

// engineResult sends res as the engine's own RunCompleted and returns the
// terminal the watch stream carries for the run.
func (r *stopCauseRig) engineResult(t *testing.T, res Result) Result {
	t.Helper()
	r.seq++
	r.c.HandleEvent(r.ch, Event{RunID: r.runID, Seq: r.seq, Payload: RunCompleted{Result: &res}})
	return r.terminal(t)
}

const stopByReason = "stopped by owner-harp: milestone landed"

// (a) The engine's CANCELLED result arrives while the stop is in progress
// (stopRun's pending detail, set before the runner is asked): the result
// every viewer sees names the stopper and the reason, and keeps the engine's
// own status and exit code.
func TestStopCause_EngineResultFirstCarriesTheStop(t *testing.T) {
	r := newStopCauseRig(t)
	r.c.setPendingStop(r.runID, stopByReason)
	t.Cleanup(func() { r.c.setPendingStop(r.runID, "") })

	code := int32(130)
	res := r.engineResult(t, Result{Status: RunStatusCancelled, ExitCode: &code})

	assert.Equal(t, RunStatusCancelled, res.Status, "the engine's status stands")
	require.NotNil(t, res.ExitCode)
	assert.EqualValues(t, 130, *res.ExitCode, "the engine's exit code stands")
	require.NotNil(t, res.Error, "a stopped run's terminal must say why it ended")
	assert.Contains(t, res.Error.Message, stopByReason)
	assert.Contains(t, res.Error.Message, CauseStopped)
}

// (a') The engine's result reaches the hub after the end is journaled but
// before the synthesized one (the terminal's tail drain): the journaled cause
// is the one it carries.
func TestStopCause_EngineResultAfterTheJournaledEndCarriesIt(t *testing.T) {
	r := newStopCauseRig(t)
	_, won := r.c.claimRunTerminal(r.runID, CauseDrainInterrupted, "coordinator drain: turn still running after the 10m0s bound; interrupted", nil)
	require.True(t, won)

	res := r.engineResult(t, Result{Status: RunStatusCancelled})

	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, CauseDrainInterrupted)
	assert.Contains(t, res.Error.Message, "interrupted")
}

// (b) The engine's own account of its end is never overwritten.
func TestStopCause_EngineErrorMessageIsKept(t *testing.T) {
	r := newStopCauseRig(t)
	r.c.setPendingStop(r.runID, stopByReason)
	t.Cleanup(func() { r.c.setPendingStop(r.runID, "") })

	res := r.engineResult(t, Result{Status: RunStatusCancelled, Error: &Status{Code: 1, Message: "engine: turn aborted by SIGINT"}})

	require.NotNil(t, res.Error)
	assert.Equal(t, "engine: turn aborted by SIGINT", res.Error.Message)
	assert.EqualValues(t, 1, res.Error.Code)
}

// (c) A cancelled result whose cause the coordinator does not know is passed
// through as the engine sent it: nothing invents a reason.
func TestStopCause_UnknownCauseLeavesTheResultAlone(t *testing.T) {
	r := newStopCauseRig(t)

	res := r.engineResult(t, Result{Status: RunStatusCancelled})

	assert.Equal(t, RunStatusCancelled, res.Status)
	assert.Nil(t, res.Error, "no known cause: the result must not claim one")
}

// A successful result is a success, whatever is pending: it gains no error.
func TestStopCause_SucceededResultGainsNoError(t *testing.T) {
	r := newStopCauseRig(t)
	r.c.setPendingStop(r.runID, stopByReason)
	t.Cleanup(func() { r.c.setPendingStop(r.runID, "") })

	res := r.engineResult(t, Result{Status: RunStatusSucceeded, Text: "done"})

	assert.Equal(t, RunStatusSucceeded, res.Status)
	assert.Nil(t, res.Error)
}

// End to end: a real agent_stop with a reason, its engine interrupted by its
// runner. Whichever terminal wins the watch stream, and in the parent's
// child-ended notice and the session's run end, the stop names its caller
// and its reason.
func TestStopCause_AgentStopReasonReachesEveryConsumer(t *testing.T) {
	r := newStopCauseRig(t)
	owner := ownerIdentity()

	_, err := r.c.AgentStop(owner, r.harp, "milestone landed", 2*time.Second)
	require.NoError(t, err)
	want := "stopped by " + owner.Harp + ": milestone landed"

	res := r.terminal(t)
	assert.Equal(t, RunStatusCancelled, res.Status)
	require.NotNil(t, res.Error, "the watch stream's terminal must say who stopped the run")
	assert.Contains(t, res.Error.Message, want)

	// The child-ended notice: the interrupted turn's own report may come
	// first, from the same child, so read until the notice is in.
	var notice string
	require.Eventually(t, func() bool {
		msgs, err := ownerMail(t, r.c, 0)
		if err != nil && !errors.Is(err, errNoOwnerMail) {
			require.NoError(t, err)
		}
		for _, m := range msgs {
			if m.From == r.harp && m.Kind == KindExited {
				notice = m.Body
			}
		}
		return notice != ""
	}, conformanceWait, 10*time.Millisecond, "the parent never got the child-ended notice")
	assert.Contains(t, notice, want, "the parent's child-ended notice")

	require.Eventually(t, func() bool {
		r.sp.mu.Lock()
		defer r.sp.mu.Unlock()
		for _, e := range r.sp.runEnds {
			if e.harp == r.harp && e.end.Cause == CauseStopped && strings.Contains(e.end.Detail, want) && e.end.Failed {
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the session's run end (run_ends) must record the stop's cause and reason")
}
