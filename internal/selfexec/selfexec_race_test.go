package selfexec

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPath_ConcurrentOverrideAccessIsRaceFree pins that the
// override global is written by an EXPORTED test-only mutator and read by
// Path, which every production spawn path calls (the MCP server entry, hook
// command materialization, agent launch). Nothing about the seam confines
// those to one goroutine, and this package is a dependency of every one of
// them.
//
// Run under -race (just test-pkg's default) an unguarded global fails here as
// `WARNING: DATA RACE`, not as an assertion. Note the converse does not hold:
// a clean -race run is not proof of absence, since the detector only reports
// interleavings that actually occurred. The argument for correctness is the
// mutex, and this is the tripwire on removing it.
func TestPath_ConcurrentOverrideAccessIsRaceFree(t *testing.T) {
	restore := SetPathForTesting("ctxloom")
	t.Cleanup(restore)

	// The fixture must be hostile: if Path did not consult the override at
	// all, the readers below would never touch the contended global.
	require.Equal(t, "ctxloom", Path(), "Path must be reading the override for this to contend")

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				undo := SetPathForTesting("ctxloom")
				undo()
				return
			}
			assert.NotEmpty(t, Path())
		}(i)
	}
	close(start)
	wg.Wait()
}
