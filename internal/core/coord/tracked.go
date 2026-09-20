package coord

import (
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// trackedGroup is this package's goroutine-ownership discipline, shared by every
// long-lived type here (Coordinator, Home, EngineHost, RunnerLink): each owns
// background goroutines that outlive the call that spawned them, and must prove
// none is still running before tearing down the state they touch —
// flaky-agentcoord was a fire-and-forget `go` racing exactly that teardown.
//
// Three rules, all load-bearing:
//
//   - dispatch TRACKS. A goroutine that skips it makes the join prove nothing.
//   - seal comes BEFORE the join. A still-live handler's deferred cleanup can
//     dispatch after its transport is torn down, concurrently with the join, and
//     wg.Add racing an in-progress wg.Wait is a sync.WaitGroup misuse (caught by
//     -race). Past the seal fn still RUNS, untracked: the dispatchers are
//     cleanups that cannot be told to stand down, and every tracked loop already
//     respects its own context.
//   - the join is BOUNDED. One wedged handler must not hang shutdown forever.
//
// Budget and diagnostic wording stay with the OWNER (passed to wait): they are
// per-owner policy, the coordinator's being deliberately the most generous.
type trackedGroup struct {
	rep     report.Reporter // the owner's Reporter, set when the owner is built
	mu      sync.Mutex      // guards closing, and serializes wg.Add against seal
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

// enter takes a slot for a goroutine the owner did NOT dispatch — a stream
// handler the gRPC server runs — so wait can join its deferred teardown.
func (g *trackedGroup) enter() (done func(), ok bool) {
	g.wg.Add(1)
	return g.wg.Done, true
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
// waitBounded waits for wg up to budget, warning and proceeding past it —
// the same discipline trackedGroup.wait applies, for a group that is not
// sealed (the stream handlers are dispatched by the gRPC server, not by us).
func waitBounded(rep report.Reporter, wg *sync.WaitGroup, budget time.Duration, what string) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
		rep.Warnf("%s: handlers did not finish within %s; proceeding (a late terminal may still touch the state dir)", what, budget)
	}
}

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
			g.rep.Warnf("%s: tracked goroutines did not finish within %s; proceeding (%s)", what, budget, risk)
			return
		}
		g.rep.Warnf("%s: tracked goroutines did not finish within %s; proceeding", what, budget)
	}
}
