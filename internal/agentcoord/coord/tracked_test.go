package coord

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Four long-lived types in this package own background goroutines under the
// same discipline: dispatch tracked, refuse to track once teardown has begun,
// and join with a bounded escape. flaky-agentcoord's root cause was an
// unjoined goroutine racing a teardown, so the discipline is load-bearing and
// all four must implement it identically — these tests drive all four through
// one interface so no owner can quietly implement it differently.

// trackedOwner is the discipline's surface. Each implementation is exercised
// on a zero-value shell: goTracked/waitTracked touch only the owner's mutex,
// WaitGroup and closing flag, never any constructed state.
type trackedOwner interface {
	goTracked(func())
	waitTracked()
}

func trackedOwners() map[string]trackedOwner {
	return map[string]trackedOwner{
		"Coordinator": &Coordinator{},
		"Home":        &Home{},
		"EngineHost":  &EngineHost{},
		"RunnerLink":  &RunnerLink{},
	}
}

// TestTrackedOwners_WaitJoinsEveryDispatchedGoroutine is the whole point of the
// discipline: when waitTracked returns, nothing dispatched before it is still
// running, so a teardown that follows cannot race a live goroutine.
func TestTrackedOwners_WaitJoinsEveryDispatchedGoroutine(t *testing.T) {
	for name, owner := range trackedOwners() {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			var mu sync.Mutex
			finished := 0
			for range 8 {
				owner.goTracked(func() {
					<-release
					mu.Lock()
					finished++
					mu.Unlock()
				})
			}
			close(release)
			owner.waitTracked()
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, 8, finished, "waitTracked returned while dispatched goroutines were still running")
		})
	}
}

// TestTrackedOwners_SealedDispatchStillRunsButIsNotJoined pins the sealed
// window's exact semantics: past the point teardown began, a fresh dispatch
// must NOT reach wg.Add (Add racing an in-progress Wait is a sync.WaitGroup
// misuse) yet must still run, because the dispatchers are deferred cleanups
// that cannot be told to stand down.
func TestTrackedOwners_SealedDispatchStillRunsButIsNotJoined(t *testing.T) {
	for name, owner := range trackedOwners() {
		t.Run(name, func(t *testing.T) {
			sealTracking(t, owner)
			ran := make(chan struct{})
			owner.goTracked(func() { close(ran) })
			select {
			case <-ran:
			case <-time.After(2 * time.Second):
				t.Fatal("a dispatch past the seal must still run, untracked and best-effort")
			}
			// Untracked: the join has nothing to wait for and returns at once.
			done := make(chan struct{})
			go func() { owner.waitTracked(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("waitTracked blocked on a goroutine dispatched past the seal")
			}
		})
	}
}

// TestTrackedOwners_JoinBudgetsAreTheDocumentedValues: the bounded escape is
// what stops one wedged handler hanging shutdown forever. The coordinator's is
// deliberately the most generous (it owns the state dir teardown behind it);
// the three runner-side owners share a tighter one. Unifying them would be a
// behaviour change, not a refactor.
func TestTrackedOwners_JoinBudgetsAreTheDocumentedValues(t *testing.T) {
	assert.Equal(t, 5*time.Second, closeJoinBudget)
	assert.Equal(t, 3*time.Second, homeCloseJoinBudget)
	assert.Equal(t, 3*time.Second, engineHostCloseJoinBudget)
	assert.Equal(t, 3*time.Second, runnerLinkCloseJoinBudget)
}

// sealTracking begins teardown on owner the way its own teardown path does,
// without running the rest of that teardown (which needs constructed state).
func sealTracking(t *testing.T, owner trackedOwner) {
	t.Helper()
	switch o := owner.(type) {
	case *Coordinator:
		o.tracked.seal()
	case *Home:
		o.tracked.seal()
	case *EngineHost:
		o.tracked.seal()
	case *RunnerLink:
		o.tracked.seal()
	default:
		require.Fail(t, "unknown tracked owner", "%T", owner)
	}
}

// TestTrackedGroup_BoundedJoinGivesUpAndSaysSo pins the escape the four owners'
// budgets feed: a goroutine that never finishes must not hang the teardown, and
// going quiet about it is what let a wedged handler look like a slow one.
func TestTrackedGroup_BoundedJoinGivesUpAndSaysSo(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	var g trackedGroup
	block := make(chan struct{})
	defer close(block)
	g.dispatch(func() { <-block })

	start := time.Now()
	g.wait(50*time.Millisecond, "test teardown", "a leaked goroutine may still touch test state")
	assert.Less(t, time.Since(start), 2*time.Second, "the join must give up on its budget, not block on the goroutine")
	assert.Contains(t, buf.String(), "test teardown")
	assert.Contains(t, buf.String(), "a leaked goroutine may still touch test state")
}

// TestTrackedGroup_BoundedJoinOmitsAnEmptyRiskClause: three of the four owners
// pass no risk clause, and the diagnostic must not sprout an empty parenthetical.
func TestTrackedGroup_BoundedJoinOmitsAnEmptyRiskClause(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	var g trackedGroup
	block := make(chan struct{})
	defer close(block)
	g.dispatch(func() { <-block })
	g.wait(50*time.Millisecond, "test teardown", "")

	assert.Contains(t, buf.String(), "test teardown")
	assert.NotContains(t, buf.String(), "()")
}
