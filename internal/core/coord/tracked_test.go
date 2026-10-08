package coord

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/trackedtest"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// The coordinator owns background goroutines under the discipline
// trackedtest drives (the runner side's owners are exercised by the same
// driver from their own package); the coordinator's join budget is
// deliberately the most generous, because it owns the state dir teardown
// behind it — the runner side's owners share a tighter one, and unifying
// them would be a behaviour change, not a refactor.
func TestTrackedOwners_Coordinator(t *testing.T) {
	c := &Coordinator{}
	trackedtest.RunOwnerTests(t, ErrGroupSealed, map[string]trackedtest.Owner{
		"Coordinator": {Dispatch: c.goTracked, Wait: c.waitTracked, Seal: c.tracked.Seal},
	})
	assert.Equal(t, 5*time.Second, closeJoinBudget)
}

// TestTrackedGroup_BoundedJoinGivesUpAndSaysSo pins the escape the four owners'
// budgets feed: a goroutine that never finishes must not hang the teardown, and
// going quiet about it is what let a wedged handler look like a slow one.
func TestTrackedGroup_BoundedJoinGivesUpAndSaysSo(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	g := TrackedGroup{rep: termRep()}
	block := make(chan struct{})
	defer close(block)
	require.NoError(t, g.Dispatch(func() { <-block }))

	start := time.Now()
	g.Wait(50*time.Millisecond, "test teardown", "a leaked goroutine may still touch test state")
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

	g := TrackedGroup{rep: termRep()}
	block := make(chan struct{})
	defer close(block)
	require.NoError(t, g.Dispatch(func() { <-block }))
	g.Wait(50*time.Millisecond, "test teardown", "")

	assert.Contains(t, buf.String(), "test teardown")
	assert.NotContains(t, buf.String(), "()")
}

// TestTrackedGroup_EnterAfterSealIsRefused pins the stream-handler half of the
// discipline. A gRPC stream handler is dispatched by the SERVER, not by
// dispatch, so it takes its slot with enter — and a handler arriving after the
// owner sealed is REFUSED, never counted: a WaitGroup.Add racing an in-progress
// Wait is the -race finding Coordinator.Close used to produce.
func TestTrackedGroup_EnterAfterSealIsRefused(t *testing.T) {
	g := TrackedGroup{rep: termRep()}
	g.Seal()
	done, ok := g.enter()
	assert.False(t, ok, "a slot taken after the seal would Add into the join")
	assert.Nil(t, done)
}

// TestTrackedGroup_EnterBeforeSealIsJoined: a slot taken before the seal holds
// the join until it is released, so a handler's deferred teardown finishes
// before the owner proceeds past wait.
func TestTrackedGroup_EnterBeforeSealIsJoined(t *testing.T) {
	g := TrackedGroup{rep: termRep()}
	done, ok := g.enter()
	require.True(t, ok)

	g.Seal()
	joined := make(chan struct{})
	go func() {
		g.Wait(time.Second, "test teardown", "")
		close(joined)
	}()
	select {
	case <-joined:
		t.Fatal("the join returned while a slot was still held")
	case <-time.After(20 * time.Millisecond):
	}
	done()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("the join did not return once the slot was released")
	}
}

// TestTrackedGroup_DispatchAfterSealIsRefused: past the seal a dispatch must
// not run at all. Run untracked, it outlives the join that was supposed to
// prove the owner quiet — a StartRun served after its runner was torn down.
func TestTrackedGroup_DispatchAfterSealIsRefused(t *testing.T) {
	g := TrackedGroup{rep: termRep()}
	g.Seal()
	ran := make(chan struct{})
	err := g.Dispatch(func() { close(ran) })
	require.ErrorIs(t, err, ErrGroupSealed)
	g.Wait(time.Second, "test teardown", "")
	select {
	case <-ran:
		t.Fatal("a dispatch past the seal ran")
	case <-time.After(100 * time.Millisecond):
	}
}
