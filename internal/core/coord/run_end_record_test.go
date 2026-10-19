package coord

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every run's end is RECORDED, whatever ended it: in the journal, on the watch
// stream every viewer of the run reads (the `ctxloom run` that owns it above
// all), in the parent's mailbox, and in the session index — with the runner's
// exit status when one is known.
//
// The verprop incident (2026-10-13): an OOM storm stalled the host; the
// coordinator declared all three runners lost ("missed heartbeats past the
// loss bound") and journaled it, but the owner's `ctxloom run` waited for a
// RunCompleted that only an engine ever sent, and printed "the runner exited
// but the run … never reported its outcome"; the session index said only
// ended_at; and nothing anywhere carried an exit status.

// killedByKernel is the error a runner handle's Wait reports for a runner the
// kernel SIGKILLed (the OOM killer's signal): a real *exec.ExitError, wrapped
// the way isolation's direct runner wraps it and classified the way it
// classifies it (exitstatus.Classify: 128+SIGKILL).
func killedByKernel(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "kill -KILL $$").Run()
	var ee *exec.ExitError
	require.True(t, errors.As(err, &ee), "a SIGKILLed shell reports an *exec.ExitError, got %v", err)
	return classifiedExit{code: 137, err: fmt.Errorf("runner container %q exited: %w", "ctxloom-child", err)}
}

// TestWatchHub_OneTerminalPerRun pins the hub's exactly-once terminal: the
// coordinator's synthesized RunCompleted and an engine's own can both reach
// broadcast for one run (a terminal on the wire as the run is ended), and a
// viewer must see one.
func TestWatchHub_OneTerminalPerRun(t *testing.T) {
	h := newWatchHub(termRep())
	events, cancel, _ := h.subscribe(nil)
	defer cancel()

	h.broadcast(Event{RunID: "run-a", Seq: 7, Payload: RunCompleted{Result: &Result{Status: RunStatusSucceeded}}})
	h.broadcast(Event{RunID: "run-a", Payload: RunCompleted{Result: &Result{Status: RunStatusFailed}}})
	h.broadcast(Event{RunID: "run-b", Payload: RunCompleted{Result: &Result{Status: RunStatusFailed}}})

	require.Len(t, events, 2, "one terminal per run")
	first := <-events
	assert.Equal(t, "run-a", first.RunID)
	assert.Equal(t, RunStatusSucceeded, first.Payload.(RunCompleted).Result.Status, "the first terminal wins")
	assert.Equal(t, "run-b", (<-events).RunID)

	// A run the coordinator has forgotten (reaped) holds no terminal slot.
	h.forgetTerminals([]string{"run-a"})
	h.broadcast(Event{RunID: "run-a", Payload: RunCompleted{Result: &Result{Status: RunStatusFailed}}})
	assert.Len(t, events, 1)
}

// TestSyntheticResult maps a coordinator-decided end onto the Result a viewer
// renders: success is an allow-list of the clean causes, a forced or
// operator end is CANCELLED, and the exit status rides along when known.
func TestSyntheticResult(t *testing.T) {
	code := int32(137)
	zero := int32(0)
	one := int32(1)
	cases := []struct {
		name   string
		cause  string
		exit   *int32
		status RunStatus
	}{
		{"runner loss", CauseRunnerLoss, &code, RunStatusFailed},
		{"launch failed", CauseLaunchFailed, nil, RunStatusFailed},
		{"stopped", CauseStopped, nil, RunStatusCancelled},
		{"drain interrupted", CauseDrainInterrupted, nil, RunStatusCancelled},
		{"clean engine exit", CauseRunnerExit, &zero, RunStatusSucceeded},
		{"engine exit non-zero", CauseRunnerExit, &code, RunStatusFailed},
		{"engine exit status 1", CauseRunnerExit, &one, RunStatusFailed},
		{"engine exit, status unknown", CauseRunnerExit, nil, RunStatusFailed},
		{"final reported", CauseFinalReported, nil, RunStatusSucceeded},
		{"idle reaped", CauseIdleReaped, nil, RunStatusSucceeded},
		{"drained", CauseDrained, nil, RunStatusSucceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := syntheticResult(tc.cause, "the detail", tc.exit)
			assert.Equal(t, tc.status, r.Status)
			assert.Equal(t, tc.exit, r.ExitCode)
			if tc.status == RunStatusSucceeded {
				assert.Nil(t, r.Error)
				return
			}
			require.NotNil(t, r.Error)
			assert.Contains(t, r.Error.Message, tc.cause)
			assert.Contains(t, r.Error.Message, "the detail")
		})
	}
}

// runEndRig is a coordinator with one live child whose runner PROCESS the
// test controls: die makes the runner's Wait return with exitErr (the kernel
// killed it), and the runner's channel is a dialed link the test can abort.
type runEndRig struct {
	c      *Coordinator
	sp     *fakeSpawner
	run    *RunOutcome
	events <-chan Event
	link   TestRunnerLink
}

func newRunEndRig(t *testing.T, wait func(released <-chan struct{}) error) *runEndRig {
	t.Helper()
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	sp.runnerWait = wait
	c := newTestCoordinator(t, sp, nil)
	c.runnerExitGrace = 200 * time.Millisecond

	run, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.chat(0)
		return e != nil && len(e.RecordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	_, events, cancel, _ := c.WatchRuns([]string{run.RunID})
	t.Cleanup(cancel)
	return &runEndRig{c: c, sp: sp, run: run, events: events, link: dialFakeRunner(t, c, run.RunID)}
}

// terminal waits for the run's one RunCompleted on the watch stream.
func (r *runEndRig) terminal(t *testing.T) Result {
	t.Helper()
	deadline := time.After(conformanceWait)
	for {
		select {
		case ev := <-r.events:
			if rc, ok := ev.Payload.(RunCompleted); ok && ev.RunID == r.run.RunID {
				require.NotNil(t, rc.Result)
				return *rc.Result
			}
		case <-deadline:
			t.Fatalf("run %s never reported its outcome on the watch stream", r.run.RunID)
		}
	}
}

// exited blocks until the coordinator's watcher has observed the runner
// process's exit, so a test orders "the kernel killed it" before "its channel
// dropped" instead of racing that watcher goroutine against runnerExitGrace —
// which a loaded machine lost, recording the kill with no exit status.
func (r *runEndRig) exited(t *testing.T) {
	t.Helper()
	var x *runnerExit
	r.c.mu.Lock()
	if rt := r.c.attach[r.run.RunID]; rt != nil {
		x = rt.runnerExit
	}
	r.c.mu.Unlock()
	require.NotNil(t, x, "the rig's runner process is watched")
	select {
	case <-x.done:
	case <-time.After(conformanceWait):
		t.Fatalf("run %s: the runner's exit was never observed", r.run.RunID)
	}
}

func (r *runEndRig) record(t *testing.T) RunRecord {
	t.Helper()
	var rec RunRecord
	require.Eventually(t, func() bool {
		r.c.runs.View(func() {
			if p := r.c.runsF.run(r.run.RunID); p != nil {
				rec = *p
			}
		})
		return rec.Ended
	}, conformanceWait, 10*time.Millisecond)
	return rec
}

// TestRunEnd_KilledRunnerIsRecordedWithItsSignalEverywhere is the OOM kill:
// the runner process is SIGKILLed, its channel drops, and the coordinator
// synthesizes runner loss. The end must say the runner was killed, with
// status 137, in all four places.
func TestRunEnd_KilledRunnerIsRecordedWithItsSignalEverywhere(t *testing.T) {
	killed := killedByKernel(t)
	die := make(chan struct{})
	rig := newRunEndRig(t, func(released <-chan struct{}) error {
		select {
		case <-die:
			return killed
		case <-released:
			return nil
		}
	})

	close(die)       // the kernel kills the runner process…
	rig.exited(t)    // …the coordinator's watcher sees it die…
	rig.link.Abort() // …and its channel drops with it.

	res := rig.terminal(t)
	assert.Equal(t, RunStatusFailed, res.Status)
	require.NotNil(t, res.ExitCode, "the watch stream carries the runner's exit status")
	assert.EqualValues(t, 137, *res.ExitCode)
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, CauseRunnerLoss)
	assert.Contains(t, res.Error.Message, "signal: killed")

	rec := rig.record(t)
	assert.Equal(t, CauseRunnerLoss, rec.Cause)
	assert.Contains(t, rec.Detail, "signal: killed", "the journal's detail names the signal")
	require.NotNil(t, rec.ExitCode, "the journal carries the exit status")
	assert.EqualValues(t, 137, *rec.ExitCode)

	msgs, err := ownerMail(t, rig.c, time.Second)
	require.NoError(t, err)
	var notice string
	for _, m := range msgs {
		if m.From == rig.run.Harp {
			notice = m.Body
		}
	}
	assert.Contains(t, notice, "signal: killed", "the parent's notice says how its child died")

	end := rig.sp.runEndFor(t, rig.run.Harp, rig.run.RunID)
	assert.Equal(t, CauseRunnerLoss, end.Cause)
	assert.True(t, end.Failed)
	require.NotNil(t, end.ExitCode, "the session index carries the exit status")
	assert.EqualValues(t, 137, *end.ExitCode)
	assert.Contains(t, end.Detail, "signal: killed")
}

// TestRunEnd_RunnerExitReapedJustAfterTheLossIsStillRecorded: the channel
// drops as the kernel kills the runner, but the reap that reports the signal
// (the container runtime's own client exiting) can trail it. The end waits for
// it, within its grace, rather than recording a runner that "was still
// running".
func TestRunEnd_RunnerExitReapedJustAfterTheLossIsStillRecorded(t *testing.T) {
	killed := killedByKernel(t)
	die := make(chan struct{})
	rig := newRunEndRig(t, func(released <-chan struct{}) error {
		select {
		case <-die:
			return killed
		case <-released:
			return nil
		}
	})

	// The grace must outlast the reap's delay however loaded the machine is:
	// at the rig's 200ms, a starved timer or watcher goroutine landed the
	// exit after the grace and the end was recorded as "still running".
	rig.c.mu.Lock()
	rig.c.runnerExitGrace = conformanceWait
	rig.c.mu.Unlock()

	rig.link.Abort()
	time.AfterFunc(50*time.Millisecond, func() { close(die) })

	res := rig.terminal(t)
	require.NotNil(t, res.ExitCode, "the exit reaped inside the grace is the one recorded")
	assert.EqualValues(t, 137, *res.ExitCode)
	assert.Contains(t, rig.record(t).Detail, "signal: killed")
}

// TestRunEnd_StalledRunnerDeclaredLostSaysItWasStillRunning is the other half
// of the verprop incident: a runner that stopped heartbeating but whose
// process had NOT exited is declared lost and ended by the coordinator. The
// record must say so, rather than imply the runner died on its own.
func TestRunEnd_StalledRunnerDeclaredLostSaysItWasStillRunning(t *testing.T) {
	rig := newRunEndRig(t, func(released <-chan struct{}) error {
		<-released
		return nil
	})

	rig.link.Abort()

	res := rig.terminal(t)
	assert.Equal(t, RunStatusFailed, res.Status)
	assert.Nil(t, res.ExitCode, "no exit was observed before the run was ended")
	rec := rig.record(t)
	assert.Contains(t, rec.Detail, "still running", "the record says the runner had not exited when it was declared lost")
	assert.Nil(t, rec.ExitCode)
	end := rig.sp.runEndFor(t, rig.run.Harp, rig.run.RunID)
	assert.Contains(t, end.Detail, "still running")
}

// TestRunEnd_EnginesOwnTerminalIsNotDoubled: a RunCompleted the engine sent
// reaches viewers as itself, and the coordinator's end adds no second one.
func TestRunEnd_EnginesOwnTerminalIsNotDoubled(t *testing.T) {
	rig := newRunEndRig(t, nil)
	engineCode := int32(0)
	rig.c.watch.broadcast(Event{RunID: rig.run.RunID, Seq: 99, Payload: RunCompleted{Result: &Result{Status: RunStatusSucceeded, Text: "engine's own", ExitCode: &engineCode}}})

	rig.c.terminateRun(rig.run.RunID, CauseRunnerExit, "")

	res := rig.terminal(t)
	assert.Equal(t, "engine's own", res.Text)
	rig.record(t)
	select {
	case ev := <-rig.events:
		_, terminal := ev.Payload.(RunCompleted)
		assert.False(t, terminal, "a second terminal reached the viewer: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRunEnd_ExplicitExitCarriesTheEnginesStatus: a RunExited frame's exit
// status is the one recorded, in the journal and the session index.
func TestRunEnd_ExplicitExitCarriesTheEnginesStatus(t *testing.T) {
	rig := newRunEndRig(t, nil)
	var cred string
	rig.c.runs.View(func() { cred = rig.c.runsF.run(rig.run.RunID).CredHash })

	rig.c.RunnerExited(cred, RunExited{RunID: rig.run.RunID, ExitCode: 3})

	res := rig.terminal(t)
	assert.Equal(t, RunStatusFailed, res.Status)
	require.NotNil(t, res.ExitCode)
	assert.EqualValues(t, 3, *res.ExitCode)
	rec := rig.record(t)
	require.NotNil(t, rec.ExitCode)
	assert.EqualValues(t, 3, *rec.ExitCode)
	end := rig.sp.runEndFor(t, rig.run.Harp, rig.run.RunID)
	require.NotNil(t, end.ExitCode)
	assert.EqualValues(t, 3, *end.ExitCode)
	assert.True(t, strings.Contains(end.Detail, "exit status 3"), end.Detail)
}

// TestRunEnd_CleanExplicitExitIsRecordedAsSucceeded pins the other half of
// RunnerExited's status recording: an engine that exits 0 with no signal
// reports its status too, so the run ends succeeded with exit code 0 rather
// than as a runner exit whose status is unknown (which reads as failed).
func TestRunEnd_CleanExplicitExitIsRecordedAsSucceeded(t *testing.T) {
	rig := newRunEndRig(t, nil)
	var cred string
	rig.c.runs.View(func() { cred = rig.c.runsF.run(rig.run.RunID).CredHash })

	rig.c.RunnerExited(cred, RunExited{RunID: rig.run.RunID, ExitCode: 0})

	res := rig.terminal(t)
	assert.Equal(t, RunStatusSucceeded, res.Status)
	require.NotNil(t, res.ExitCode)
	assert.EqualValues(t, 0, *res.ExitCode)
	end := rig.sp.runEndFor(t, rig.run.Harp, rig.run.RunID)
	require.NotNil(t, end.ExitCode)
	assert.EqualValues(t, 0, *end.ExitCode)
	assert.False(t, end.Failed, "a clean exit is not a failed run")
}

// runEndFor waits for the run end recorded for harp's run runID.
func (s *fakeSpawner) runEndFor(t *testing.T, harp, runID string) sessions.RunEnd {
	t.Helper()
	var got *sessions.RunEnd
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, c := range s.runEnds {
			if c.harp == harp && c.end.RunID == runID {
				e := c.end
				got = &e
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "no run end was recorded for %s's run %s", harp, runID)
	return *got
}

// runnerHeld is a runner process that runs until the coordinator releases it.
func runnerHeld(released <-chan struct{}) error {
	<-released
	return nil
}

// TestRunEnd_ASignalledExitCarriesItsCodeAndACodelessOneNone: an engine
// killed by a signal reports the signal, and its status when it has one — 137
// is recorded; a signal with status 0 carries no status to record, so the end
// records none rather than a clean 0.
func TestRunEnd_ASignalledExitCarriesItsCodeAndACodelessOneNone(t *testing.T) {
	for name, tc := range map[string]struct {
		code int32
		want *int32
	}{
		"with a status": {code: 137, want: func() *int32 { v := int32(137); return &v }()},
		"no status":     {code: 0, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newRunEndRig(t, runnerHeld)
			var cred string
			rig.c.runs.View(func() { cred = rig.c.runsF.run(rig.run.RunID).CredHash })

			rig.c.RunnerExited(cred, RunExited{RunID: rig.run.RunID, Signal: "SIGKILL", ExitCode: tc.code})

			res := rig.terminal(t)
			assert.Equal(t, tc.want, res.ExitCode)
			assert.Equal(t, tc.want, rig.record(t).ExitCode)
		})
	}
}

// TestRunEndStatus_OnlyALossWithNoReportedStatusAwaitsTheRunner: the runner
// process's exit is awaited (and folded into the detail) only for a run
// declared lost whose engine reported no status, and only while the run has
// not already ended. Every other end is answered at once, its detail as given.
func TestRunEndStatus_OnlyALossWithNoReportedStatusAwaitsTheRunner(t *testing.T) {
	rig := newRunEndRig(t, runnerHeld)
	c, runID := rig.c, rig.run.RunID
	c.runnerExitGrace = conformanceWait // an await would show as a stall and a changed detail

	quick := func(cause string) (string, *int32) {
		t.Helper()
		start := time.Now()
		detail, code := c.runEndStatus(runID, cause, "the detail")
		require.Less(t, time.Since(start), conformanceWait/2, "%s: the runner's exit was awaited", cause)
		return detail, code
	}

	detail, code := quick(CauseStopped)
	assert.Equal(t, "the detail", detail, "a coordinator-decided end is not the runner's to describe")
	assert.Nil(t, code)

	reported := int32(5)
	c.mu.Lock()
	c.attach[runID].reportedExit = &reported
	c.mu.Unlock()
	detail, code = quick(CauseRunnerLoss)
	assert.Equal(t, "the detail", detail, "the engine's own status is the run's")
	assert.Equal(t, &reported, code)
	c.mu.Lock()
	c.attach[runID].reportedExit = nil
	c.mu.Unlock()

	// A terminal already won: this end will not be recorded, so nothing waits.
	// Set (and later cleared) under the store's write lock, journaling nothing.
	setEnded := func(ended bool) {
		require.NoError(t, c.runs.Exec(func() ([]Fact, error) {
			c.runsF.run(runID).Ended = ended
			return nil, nil
		}))
	}
	setEnded(true)
	defer setEnded(false)
	detail, code = quick(CauseRunnerLoss)
	assert.Equal(t, "the detail", detail)
	assert.Nil(t, code)
}

// TestJoinDetail: more is appended to a detail after "; ", and stands alone
// when there is no detail.
func TestJoinDetail(t *testing.T) {
	assert.Equal(t, "more", joinDetail("", "more"))
	assert.Equal(t, "detail; more", joinDetail("detail", "more"))
}

// TestDescribeRunnerExit names each of the runner process's three ends.
func TestDescribeRunnerExit(t *testing.T) {
	assert.Equal(t, "runner process still running when the run was ended (the coordinator ended it)", describeRunnerExit(nil, false))
	assert.Equal(t, "runner process exited with status 0", describeRunnerExit(nil, true))
	assert.Equal(t, "runner process: boom", describeRunnerExit(errors.New("boom"), true))
}

// TestLaunchFailureWarning_PointsBackOnlyWhenItHasASummary: a refusal whose
// findings the stream already showed is named by its summary; one marked
// Streamed with no summary has nothing to name, so the full detail is
// written, as for any other error.
func TestLaunchFailureWarning_PointsBackOnlyWhenItHasASummary(t *testing.T) {
	base := errors.New("refused")
	assert.Equal(t, "launch refused (the findings and their fixes are reported above)",
		launchFailureWarning(report.Streamed{Err: base, Summary: "launch refused"}, "the detail"))
	assert.Equal(t, "the detail", launchFailureWarning(report.Streamed{Err: base}, "the detail"))
	assert.Equal(t, "the detail", launchFailureWarning(base, "the detail"))
}

// TestWithEndCause_ACompletionWithNoResultIsPassedThrough: a RunCompleted
// carrying no Result has nothing to fill in, and is handed on untouched.
func TestWithEndCause_ACompletionWithNoResultIsPassedThrough(t *testing.T) {
	ev := Event{RunID: "run-1", Payload: RunCompleted{}}
	assert.Equal(t, ev, (&Coordinator{}).withEndCause("run-1", ev))
}

// TestRunnerExitReason: a runner not yet exited is not dead; one that exited
// with an error is dead for that error; one that exited cleanly is dead too,
// and says it never dialed home.
func TestRunnerExitReason(t *testing.T) {
	exited := func(err error) *runnerExit {
		x := &runnerExit{done: make(chan struct{}), err: err}
		close(x.done)
		return x
	}

	reason, dead := runnerExitReason(&runnerExit{done: make(chan struct{})})
	assert.False(t, dead)
	assert.Empty(t, reason)

	reason, dead = runnerExitReason(exited(errors.New("signal: killed")))
	assert.True(t, dead)
	assert.Equal(t, "signal: killed", reason)

	reason, dead = runnerExitReason(exited(nil))
	assert.True(t, dead)
	assert.Equal(t, "runner process exited cleanly (status 0) without ever dialing home", reason)
}
