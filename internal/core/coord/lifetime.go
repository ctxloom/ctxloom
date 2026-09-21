package coord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Runner lifetime = session. A runner outlives a one-shot turn: it parks on
// its inbox between turns with its endpoint bound, and the coordinator
// delivers the next turn to the SAME runner — mail through the spool, or a
// Turn frame — rather than starting a new run. Two things end a parked
// runner besides agent_stop, terminateRun and its own exit: the idle reaper
// (no turn for idleTimeout) and the coordinator's own death, after which the
// restarted coordinator RE-ADOPTS a runner that dials back within the
// runner-loss grace instead of orphaning it.

// defaultIdleTimeout is the idle reaper's built-in bound, the same fifteen
// minutes config.DefaultDelegationIdleTimeout resolves to when the project
// says nothing; Options.IdleTimeout carries the configured value.
const defaultIdleTimeout = 15 * time.Minute

// idleReapInterval is how often the reaper sweeps; a run idles for at least
// idleTimeout and at most idleTimeout + this before it is ended.
const idleReapInterval = time.Minute

// ErrNoLiveRun refuses a Turn addressed to a run id no live runner holds.
var ErrNoLiveRun = errors.New("coord: no live run by that id")

// Turn is RunnerTransport.Turn: the one-shot turn injection. The runner
// OUTLIVES a one-shot turn (runner lifetime = session), so a turn is a frame
// to the SAME runner, not a new run: it drives one engine turn on the parked
// runner and answers with the turn's result and the key the next turn
// resumes by. It blocks until the turn's boundary, bounded by ctx.
func (c *Coordinator) Turn(ctx context.Context, runID string, t engine.Turn) (engine.TurnResult, error) {
	var credHash string
	live := false
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil && !r.Ended {
			credHash, live = r.CredHash, true
		}
	})
	if !live {
		return engine.TurnResult{}, fmt.Errorf("%w: %s", ErrNoLiveRun, runID)
	}
	resp, err := c.requestRunner(ctx, credHash, RunnerRequest{Kind: TurnRequest{Turn: t}})
	if err != nil {
		return engine.TurnResult{}, fmt.Errorf("turn %s: %w", runID, err)
	}
	if resp.Err != nil {
		return engine.TurnResult{}, fmt.Errorf("turn %s refused: %s", runID, resp.Err.Error())
	}
	res, _ := resp.Kind.(TurnResult)
	return res.Result, nil
}

// idleReaper sweeps every idleReapInterval until the coordinator closes.
func (c *Coordinator) idleReaper() { c.every(idleReapInterval, c.reapIdleRuns) }

// reapIdleRuns is the idle reaper's sweep: every live run whose runner has
// sat idle (a turn boundary passed, no turn since) for idleTimeout is ended
// with CauseIdleReaped — its slot, its process (a container, on that axis)
// and its bound endpoint freed. The harp stays resumable: the next mail
// starts a new incarnation through the resume arm.
func (c *Coordinator) reapIdleRuns() {
	now := c.now()
	var reap []string
	c.mu.Lock()
	for runID, rt := range c.attach {
		if rt.idleSince.IsZero() || now.Sub(rt.idleSince) < c.idleTimeout {
			continue
		}
		reap = append(reap, runID)
	}
	c.mu.Unlock()
	for _, runID := range reap {
		if c.runState(runID) != StateIdle {
			continue // a turn started since the sweep read the clock
		}
		c.terminateRun(runID, CauseIdleReaped, fmt.Sprintf("no turn for %s", c.idleTimeout))
	}
}

// runnerConnected reports whether the runner holding runID has a live
// RunnerChannel to this coordinator.
func (c *Coordinator) runnerConnected(runID string) bool {
	var credHash string
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			credHash = r.CredHash
		}
	})
	if credHash == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.runners[credHash]
	return ok
}

// readopt gives a run this coordinator did not start — one it found live in
// the journal at startup and whose runner has just dialed back naming it —
// its runtime attachment: a childRt the terminal path can end (terminateRun
// reads attach), and its cell ownership through Spawner.Adopt (the release
// becomes the attachment's close, so the run's end releases what the dead
// process held). Idempotent per run: a runner that reconnects twice is
// adopted once.
func (c *Coordinator) readopt(runID string) {
	var rec RunRecord
	found := false
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil && !r.Ended {
			rec, found = *r, true
		}
	})
	if !found {
		return
	}
	c.mu.Lock()
	if _, attached := c.attach[runID]; attached {
		c.mu.Unlock()
		return
	}
	rt := &childRt{
		runID:       runID,
		harp:        rec.Harp,
		agentName:   rec.Agent,
		parentHarp:  rec.ParentHarp,
		parentRunID: rec.ParentRunID,
		depth:       rec.Depth,
		oneshot:     rec.OneShot,
		ownerRun:    rec.ParentHarp == rec.Harp,
		plan:        &SpawnPlan{AgentName: rec.Agent, Runtime: rec.Runtime, Permission: rec.Permission},
		attached:    make(chan struct{}),
	}
	if rec.State == StateIdle {
		rt.idleSince = c.now()
	}
	c.attach[runID] = rt
	c.byHarp[rec.Harp] = rt
	c.mu.Unlock()
	close(rt.attached)

	rec.Orchestrator = c.ownerHarp
	release, err := c.spawner.Adopt(c.baseCtx, rec)
	if err != nil {
		c.rep.Warnf("re-adopt run %s (%s): the run's cell ownership could not be re-acquired: %v", runID, rec.Harp, err)
	}
	c.mu.Lock()
	if release != nil {
		rt.close = func() {
			if rerr := release(); rerr != nil {
				c.rep.Warnf("run %s (%s): release re-adopted cell: %v", runID, rec.Harp, rerr)
			}
		}
	}
	c.mu.Unlock()
	c.audit("run_readopted", rec.Harp, map[string]string{"run_id": runID})
}

// expireRunnerGrace fires every pending runner-loss grace window at once —
// the deterministic stand-in for the clock a restart test never waits on.
func (c *Coordinator) expireRunnerGrace() {
	c.mu.Lock()
	pending := c.graceExpire
	c.graceExpire = nil
	c.mu.Unlock()
	for _, fire := range pending {
		fire()
	}
}
