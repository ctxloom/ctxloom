// Symlink tests verify the executable path resolution.
package agent

import (
	"path/filepath"
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
