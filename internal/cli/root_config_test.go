package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// GetConfig and GetConfigForUpdate share every step except which loader they
// call, so this pins the behaviour BOTH must keep: a usable config for the
// current project and the same set of downgraded-to-warning findings echoed
// from it. The one thing that must stay DIFFERENT is instance identity —
// GetConfig hands back the memoized ambient instance (~35 call sites share one
// parse) while GetConfigForUpdate hands back a mutable instance of its own, so
// a mutation abandoned on an error path cannot leak into later readers.
func TestGetConfigVariants_SharedShapeSeparateInstances(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	first, err := GetConfig()
	require.NoError(t, err)
	require.NotNil(t, first)

	again, err := GetConfig()
	require.NoError(t, err)
	assert.Same(t, first, again, "GetConfig serves the memoized ambient config")

	fresh, err := GetConfigForUpdate()
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.NotSame(t, first, fresh, "GetConfigForUpdate must own its instance so an abandoned mutation cannot leak")
	assert.Equal(t, first.GetWarnings(), fresh.GetWarnings(), "both loaders surface the same findings")
}
