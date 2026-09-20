package coord

import (
	"context"
	"errors"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// defaultIdleTimeout is the idle reaper's built-in bound, the same fifteen
// minutes config.DefaultDelegationIdleTimeout resolves to when the project
// says nothing; Options.IdleTimeout carries the configured value.
const defaultIdleTimeout = 15 * time.Minute

// ErrNoLiveRun refuses a Turn addressed to a run id no live runner holds.
var ErrNoLiveRun = errors.New("coord: no live run by that id")

// Turn is RunnerTransport.Turn: the one-shot turn injection. The runner
// OUTLIVES a one-shot turn (runner lifetime = session), so a turn is a frame
// to the SAME runner, not a new run: it drives one engine turn on the parked
// runner and answers with the turn's result and the key the next turn
// resumes by.
func (c *Coordinator) Turn(ctx context.Context, runID string, t engine.Turn) (engine.TurnResult, error) {
	return engine.TurnResult{}, ErrNoLiveRun
}

// reapIdleRuns is the idle reaper's sweep: every live run whose runner has
// had no turn for idleTimeout is ended with CauseIdleReaped.
func (c *Coordinator) reapIdleRuns() {}

// runnerConnected reports whether the runner holding runID has a live
// RunnerChannel to this coordinator.
func (c *Coordinator) runnerConnected(runID string) bool {
	return false
}

// expireRunnerGrace fires every pending runner-loss grace window at once —
// the deterministic stand-in for the clock a restart test never waits on.
func (c *Coordinator) expireRunnerGrace() {}
