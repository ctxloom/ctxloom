// Package trackedtest is the goroutine-owner discipline test, shared by every
// package that has long-lived types owning background goroutines under it:
// dispatch tracked, refuse to track once teardown has begun, and join with a
// bounded escape. An unjoined goroutine racing a teardown is the worst flake
// class these suites have had, so the discipline is load-bearing and every
// owner must implement it identically — one driver exercises them all, so no
// owner can quietly implement it differently.
package trackedtest

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Owner is one type's discipline surface, exercised on a zero-value shell:
// the three funcs touch only the owner's group, never any constructed state.
type Owner struct {
	// Dispatch runs fn on a tracked goroutine.
	Dispatch func(fn func())
	// Wait joins every dispatched goroutine, with the owner's bounded escape.
	Wait func()
	// Seal begins teardown the way the owner's own teardown path does,
	// without running the rest of that teardown.
	Seal func()
}

// RunOwnerTests drives every owner through the discipline.
func RunOwnerTests(t *testing.T, owners map[string]Owner) {
	t.Helper()
	// Wait joins every dispatched goroutine — the whole point of the
	// discipline.
	t.Run("WaitJoinsEveryDispatchedGoroutine", func(t *testing.T) {
		for name, owner := range owners {
			t.Run(name, func(t *testing.T) {
				release := make(chan struct{})
				var mu sync.Mutex
				finished := 0
				for range 8 {
					owner.Dispatch(func() {
						<-release
						mu.Lock()
						finished++
						mu.Unlock()
					})
				}
				close(release)
				owner.Wait()
				mu.Lock()
				defer mu.Unlock()
				assert.Equal(t, 8, finished, "Wait returned while dispatched goroutines were still running")
			})
		}
	})
	// The sealed window's exact semantics: past the point teardown began, a
	// fresh dispatch must NOT reach wg.Add (Add racing an in-progress Wait
	// is a sync.WaitGroup misuse) yet must still run, because the
	// dispatchers are deferred cleanups that cannot be told to stand down.
	t.Run("SealedDispatchStillRunsButIsNotJoined", func(t *testing.T) {
		for name, owner := range owners {
			t.Run(name, func(t *testing.T) {
				owner.Seal()
				ran := make(chan struct{})
				owner.Dispatch(func() { close(ran) })
				select {
				case <-ran:
				case <-time.After(2 * time.Second):
					t.Fatal("a dispatch past the seal must still run, untracked and best-effort")
				}
				// Untracked: the join has nothing to wait for and returns at once.
				done := make(chan struct{})
				go func() { owner.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("Wait blocked on a goroutine dispatched past the seal")
				}
			})
		}
	})
}
