package coord

import (
	"context"
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
	ctx, cancel := context.WithTimeout(c.baseCtx, endFenceBound)
	defer cancel()
	resp, err := c.requestRunner(ctx, rec.CredHash, RunnerRequest{Kind: PauseRun{RunID: runID, Reason: "the run is ending (" + cause + ")"}})
	if err == nil {
		err = resp.Err
	}
	if err != nil {
		c.rep.Warnf("agent %s: run %s's runner was not told its run is ending (%v); until its teardown, its own spool sweep may still deliver mail meant for the run that resumes the harp",
			rec.Harp, runID, err)
	}
	return rec
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
