package coord

import (
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// trackedGroup is this package's goroutine-ownership discipline: every long-
// lived type here (Coordinator, Home, EngineHost, RunnerLink) owns background
// goroutines that can outlive the call that spawned them, and each must be able
// to prove none of them is still running before it tears down the state they
// touch. flaky-agentcoord's root cause was exactly a fire-and-forget `go` with
// no owner racing that teardown — a prior test's Close() cancelling while a
// launch goroutine was still touching journals it had just closed — so this is
// load-bearing, not hygiene.
//
// Three rules, and all three matter:
//
//   - dispatch TRACKS. Every goroutine whose lifetime can exceed its spawning
//     call rides it, or the join below proves nothing.
//   - seal comes BEFORE the join. A still-live handler's deferred cleanup can
//     dispatch new work after the transport it belonged to is already torn down,
//     genuinely concurrently with the join — and wg.Add racing an in-progress
//     wg.Wait is a sync.WaitGroup misuse, caught live by -race. Past the seal fn
//     still RUNS, untracked and best-effort: the dispatchers are cleanups that
//     cannot be told to stand down, and every tracked loop already respects its
//     own context.
//   - the join is BOUNDED. One wedged handler must not hang shutdown forever;
//     the escape logs a leaked-goroutine diagnostic and proceeds.
//
// The group carries its own mutex rather than borrowing its owner's: the owner's
// mu guards unrelated state, and nothing outside this file needs to observe the
// sealed flag together with any of it. Budget and diagnostic wording stay with
// the OWNER (passed to wait) because they are per-owner policy — the
// coordinator's budget is deliberately the most generous, since a state-dir
// teardown waits behind it.
type trackedGroup struct {
	mu      sync.Mutex // guards closing, and serializes wg.Add against seal
	wg      sync.WaitGroup
	closing bool
}

// dispatch runs fn on a new goroutine, tracked so wait can join it — unless the
// group is already sealed, in which case fn still runs but untracked.
func (g *trackedGroup) dispatch(fn func()) {
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		go fn()
		return
	}
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		fn()
	}()
}

// seal stops tracking new dispatches. Called at the START of a teardown, before
// wait, so nothing can Add into an in-progress Wait.
func (g *trackedGroup) seal() {
	g.mu.Lock()
	g.closing = true
	g.mu.Unlock()
}

// wait joins every tracked goroutine, giving up after budget with a diagnostic
// naming what (the teardown, e.g. "coordinator close") and, when risk is
// non-empty, what a goroutine still running past the budget may still touch —
// rather than deadlocking the teardown.
func (g *trackedGroup) wait(budget time.Duration, what, risk string) {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
		if risk != "" {
			clidiag.Warn("ctxloom", "%s: tracked goroutines did not finish within %s; proceeding (%s)", what, budget, risk)
			return
		}
		clidiag.Warn("ctxloom", "%s: tracked goroutines did not finish within %s; proceeding", what, budget)
	}
}
