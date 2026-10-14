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
