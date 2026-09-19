// Symlink tests verify the executable path resolution.
package agent

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetExecutablePath(t *testing.T) {
	// Set a test path
	SetExecutablePathForTesting("/test/path/to/ctxloom")
	defer SetExecutablePathForTesting("") // Reset after test

	path, err := GetExecutablePath()
	assert.NoError(t, err)
	assert.Equal(t, "/test/path/to/ctxloom", path)
}

// TestCtxloomPathSkewed pins the comparison that drives the startup
// version-skew warning: a different ctxloom earlier on PATH than the
// running binary is the one failure bare commands can't catch.
func TestCtxloomPathSkewed(t *testing.T) {
	assert.True(t, ctxloomPathSkewed("/home/u/go/bin/ctxloom", "/usr/bin/ctxloom"),
		"a different binary on PATH is skew")
	assert.False(t, ctxloomPathSkewed("/home/u/go/bin/ctxloom", "/home/u/go/bin/ctxloom"),
		"same path is not skew")
	assert.False(t, ctxloomPathSkewed("/home/u/go/bin/ctxloom", ""),
		"not on PATH is not treated as skew")
}

// GetExecutablePath is the SYMLINK-RESOLVED, memoized answer, and both halves
// are what make it unfit for the job selfexec.Path does. It exists to detect
// PATH skew — "is the `ctxloom` PATH would run the binary running now?" — a
// comparison that is only meaningful between two fully resolved paths, and a
// resolution the caller wants performed once rather than on every check.
func TestGetExecutablePath_ResolvedAndMemoized(t *testing.T) {
	SetExecutablePathForTesting("")
	t.Cleanup(func() { SetExecutablePathForTesting("") })

	got, err := GetExecutablePath()
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.True(t, filepath.IsAbs(got), "the answer is always absolute; there is no bare-name fallback here")

	resolved, rerr := filepath.EvalSymlinks(got)
	require.NoError(t, rerr)
	assert.Equal(t, resolved, got, "symlinks are already resolved, so resolving again is a fixed point")

	again, err := GetExecutablePath()
	require.NoError(t, err)
	assert.Equal(t, got, again, "resolution is memoized for the process lifetime")
}

// cachedExecPath is a package global read by GetExecutablePath and
// written by both GetExecutablePath (memoizing) and
// SetExecutablePathForTesting, with no synchronization. A production caller
// of the ForTesting mutator, internal/operations/hooks.go's
// ApplyHooksRequest.ExecPath, was already deleted (a0c17295), so the remaining
// exposure is the unguarded global itself: the package is a dependency of every
// engine backend and nothing about the seam stops a second goroutine reaching
// it.
//
// Run under -race (just test-pkg's default) this fails as `WARNING: DATA RACE`
// against the unguarded global, not as an assertion.
func TestGetExecutablePath_ConcurrentAccessIsRaceFree(t *testing.T) {
	t.Cleanup(func() { SetExecutablePathForTesting("") })

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				SetExecutablePathForTesting("/test/path/to/ctxloom")
				return
			}
			got, err := GetExecutablePath()
			assert.NoError(t, err)
			assert.NotEmpty(t, got)
		}(i)
	}
	close(start)
	wg.Wait()
}
