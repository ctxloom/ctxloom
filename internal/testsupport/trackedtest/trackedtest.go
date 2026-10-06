// Package trackedtest is the goroutine-owner discipline test, shared by every
// package that has long-lived types owning background goroutines under it:
// dispatch tracked, refuse to run anything once teardown has begun, and join
// with a bounded escape. An unjoined goroutine racing a teardown is the worst flake
// class these suites have had, so the discipline is load-bearing and every
// owner must implement it identically — one driver exercises them all, so no
// owner can quietly implement it differently.
package trackedtest

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Owner is one type's discipline surface, exercised on a zero-value shell:
// the three funcs touch only the owner's group, never any constructed state.
type Owner struct {
	// Dispatch runs fn on a tracked goroutine, or refuses once sealed.
	Dispatch func(fn func()) error
	// Wait joins every dispatched goroutine, with the owner's bounded escape.
	Wait func()
	// Seal begins teardown the way the owner's own teardown path does,
	// without running the rest of that teardown.
	Seal func()
}

// RunOwnerTests drives every owner through the discipline. sealed is the
// refusal a dispatch past the seal must return — passed in, because the
// package that defines it tests its own owners with this driver.
func RunOwnerTests(t *testing.T, sealed error, owners map[string]Owner) {
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
					require.NoError(t, owner.Dispatch(func() {
						<-release
						mu.Lock()
						finished++
						mu.Unlock()
					}))
				}
				close(release)
				owner.Wait()
				mu.Lock()
				defer mu.Unlock()
				assert.Equal(t, 8, finished, "Wait returned while dispatched goroutines were still running")
			})
		}
	})
	// Past the point teardown began, a dispatch is REFUSED and never runs:
	// run untracked it would outlive the join that proves the owner quiet.
	t.Run("SealedDispatchIsRefused", func(t *testing.T) {
		for name, owner := range owners {
			t.Run(name, func(t *testing.T) {
				owner.Seal()
				ran := make(chan struct{})
				require.ErrorIs(t, owner.Dispatch(func() { close(ran) }), sealed)
				owner.Wait()
				select {
				case <-ran:
					t.Fatal("a dispatch past the seal ran")
				case <-time.After(100 * time.Millisecond):
				}
			})
		}
	})
}
