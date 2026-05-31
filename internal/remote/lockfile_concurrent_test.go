package remote

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLockfileManager_ConcurrentSaveNoTornFile pins jolly-unworried-twitter: the
// lockfile is the sole on-disk trust/provenance record, so a torn write would
// corrupt it. Save writes to a unique temp file and renames into place, which is
// atomic on one filesystem — so concurrent writers and readers must never
// observe a partial/empty/corrupt lock.yaml, only a complete one.
//
// This does NOT assert no-lost-writes (Save has no cross-process read-modify-write
// lock by design); it asserts every observed state is a *valid, complete* lockfile.
func TestLockfileManager_ConcurrentSaveNoTornFile(t *testing.T) {
	mgr := NewLockfileManager(t.TempDir())

	// Seed so readers racing the first writers still find a complete file.
	require.NoError(t, mgr.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{
		"seed": {SHA: "0000000000000000000000000000000000000000", URL: "u"},
	}}))

	const writers = 24
	const readers = 24

	var wg sync.WaitGroup
	readErrs := make(chan error, readers)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lf := &Lockfile{Version: 1, Bundles: map[string]LockEntry{
				fmt.Sprintf("bundle-%02d", i): {
					SHA: fmt.Sprintf("%040d", i),
					URL: fmt.Sprintf("https://example.test/repo-%02d", i),
				},
			}}
			if err := mgr.Save(lf); err != nil {
				readErrs <- fmt.Errorf("writer %d: %w", i, err)
			}
		}(i)
	}

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each Load must parse to a complete lockfile — never a torn read.
			lf, err := mgr.Load()
			if err != nil {
				readErrs <- fmt.Errorf("reader load: %w", err)
				return
			}
			if lf.Count() == 0 {
				readErrs <- fmt.Errorf("reader observed an empty (torn?) lockfile")
			}
		}()
	}

	wg.Wait()
	close(readErrs)
	for err := range readErrs {
		assert.NoError(t, err)
	}

	// Final state is one writer's complete lockfile (exactly one bundle entry),
	// proving the last rename landed a whole file.
	final, err := mgr.Load()
	require.NoError(t, err)
	assert.Equal(t, 1, final.Count(), "final lockfile should hold exactly one writer's entry")
}
