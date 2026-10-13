package coord

import (
	"context"
	"errors"
	"slices"
	"time"
)

// THE END FENCE: an ended run's runner is told its run is over BEFORE the end
// is journaled.
//
// A run's end is journaled first and torn down after (endRun): the roster
// reads ended, and mail for the harp resumes it as a fresh run, while the
// ended run's runner — its kill, then its run channel's sever, still to come
// — is alive and still reads the harp's in/ spool. Its sweep, rung by a
// doorbell or its own timer, would hand a follow-up meant for the resumed run
// to its own idle engine; the file is consumed, and the resumed run never
// sees it.
//
// The fence is the runner channel's existing PauseRun, no new wire message:
// a paused run takes no new turn, and a turn held at the gate leaves its mail
// UNCONSUMED in in/ (the runner acks a delivery only once the engine took the
// turn), so whatever the ending runner sweeps stays on disk for the run that
// resumes the harp. While the run is ending the coordinator sends it no
// ResumeRun (sendRunnerControl), which is what makes this pause final, and
// rings no doorbell on its channel (ringSpool) — the defence in depth for a
// runner the fence could not reach.
//
// The fence is skipped when the RUNNER reported the end (RunExited: its engine
// host has ended, and that answer arrives on the runner channel's own recv
// goroutine, which a request here would wait on), when the runner is gone
// (runner loss), and when the run never started on it (a launch failure).
//
// A fence still WAITING when another terminal records the run's end is
// released at once (releaseFences): that terminal either fenced the runner
// itself or needed no fence (the runner ended the run, or is gone), so the
// waiting fence has nothing left to do. It must not wait on: a stopped turn's
// boundary fences on the run channel's recv goroutine while the runner's
// RunExited, on the runner channel's, records the end and then drains that
// same run channel — each waiting on the other until terminalDrainWindow.

// errEndRecorded is the cause a waiting fence is released with when another
// terminal recorded the run's end: the fence is moot, so it is not warned
// about.
var errEndRecorded = errors.New("another terminal recorded the run's end")

// endFenceBound bounds the fence's wait for the runner's answer. A live
// runner answers a pause at once; one that does not answer is not allowed to
// hold a run's end hostage, and the end goes ahead unfenced (warned), with
// the doorbell refusal still standing.
const endFenceBound = 5 * time.Second

// fencesRunner reports whether a terminal of cause is decided while the run's
// runner still hosts the run, which is when the fence applies.
func fencesRunner(cause string) bool {
	switch cause {
	case CauseRunnerExit, CauseRunnerLoss, CauseLaunchFailed:
		return false
	}
	return true
}

// markEnding records that runID's end has begun; unmarkEnding undoes one
// mark. Counted, because two terminals of one run may race (only one claims
// it), and the first to finish must not lift the other's mark.
func (c *Coordinator) markEnding(runID string) {
	c.mu.Lock()
	c.ending[runID]++
	c.mu.Unlock()
}

func (c *Coordinator) unmarkEnding(runID string) {
	c.mu.Lock()
	if c.ending[runID]--; c.ending[runID] <= 0 {
		delete(c.ending, runID)
	}
	c.mu.Unlock()
}

// endingLocked reports whether runID's end has begun and its teardown is not
// yet done. Caller holds c.mu.
func (c *Coordinator) endingLocked(runID string) bool {
	return runID != "" && c.ending[runID] > 0
}

// fenceRunner pauses runID's runner ahead of the run's end (see THE END
// FENCE), returning the record of the run it fenced (nil when there was
// nothing to fence). Best-effort: a run already ended, or with no runner
// connected, has nothing to fence; a runner that refuses or does not answer is
// warned about, and the end goes ahead.
func (c *Coordinator) fenceRunner(runID, cause string) *RunRecord {
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil && !r.Ended && r.CredHash != "" {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil || !c.runnerConnected(runID) {
		return nil
	}
	released, release := context.WithCancelCause(c.baseCtx)
	defer c.trackFence(runID, &release)()
	// Re-checked once tracked: an end recorded before the registration found
	// no fence to release, and this one would otherwise wait it out.
	if c.runEnded(runID) {
		return rec
	}
	ctx, cancel := context.WithTimeout(released, endFenceBound)
	defer cancel()
	resp, err := c.requestRunner(ctx, rec.CredHash, RunnerRequest{Kind: PauseRun{RunID: runID, Reason: "the run is ending (" + cause + ")"}})
	if err == nil {
		err = resp.Err
	}
	if err != nil && !errors.Is(context.Cause(released), errEndRecorded) {
		c.rep.Warnf("agent %s: run %s's runner was not told its run is ending (%v); until its teardown, its own spool sweep may still deliver mail meant for the run that resumes the harp",
			rec.Harp, runID, err)
	}
	return rec
}

// trackFence registers release as a fence of runID's that releaseFences can
// end, returning its deregistration (which also releases it).
func (c *Coordinator) trackFence(runID string, release *context.CancelCauseFunc) func() {
	c.mu.Lock()
	c.fences[runID] = append(c.fences[runID], release)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if rest := slices.DeleteFunc(c.fences[runID], func(f *context.CancelCauseFunc) bool { return f == release }); len(rest) > 0 {
			c.fences[runID] = rest
		} else {
			delete(c.fences, runID)
		}
		c.mu.Unlock()
		(*release)(nil)
	}
}

// releaseFences ends every fence of runID's still waiting on its runner's
// answer. Called by the terminal that recorded the run's end, once recorded.
func (c *Coordinator) releaseFences(runID string) {
	c.mu.Lock()
	waiting := slices.Clone(c.fences[runID])
	c.mu.Unlock()
	for _, release := range waiting {
		(*release)(errEndRecorded)
	}
}

// unfenceLive lifts the fence fenceRunner set on rec's run when its end was
// NOT recorded — the journal refused the terminal — so a run that is still
// live is not left paused by an end that never happened. A run some other
// terminal ended keeps its fence: that terminal's teardown owns it.
func (c *Coordinator) unfenceLive(rec *RunRecord) {
	if rec == nil || c.runEnded(rec.RunID) {
		return
	}
	if _, err := c.sendRunnerControl(c.baseCtx, rec, "resume", ""); err != nil {
		c.rep.Warnf("agent %s: run %s stays paused: its end was not recorded and the pause the end set could not be lifted: %v", rec.Harp, rec.RunID, err)
	}
}

// isEnding is endingLocked for a caller not holding c.mu.
func (c *Coordinator) isEnding(runID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.endingLocked(runID)
}
