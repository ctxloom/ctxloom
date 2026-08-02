package config

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three set-backed accumulators a resolved Profile exports —
// ExcludeFragments, ExcludeMCP and DenyTools — reach the caller through
// collections.Set.Items(), which returns MAP-ITERATION order and Go deliberately
// randomizes that. They are not internal bookkeeping: they are Profile fields,
// and DenyTools travels on to the launch backends' settings surface, so a random
// order means the deny list in an engine's settings file is spelled differently
// on every single run. AssembleManagedDenyTools even documents the opposite
// ("Order is deterministic"). The other accumulators on this builder (Tags,
// Bundles, Commands, Skills) already went through orderedSet for exactly this
// reason; these three were left behind.
func TestResolveProfile_SetBackedFieldsAreDeterministicallyOrdered(t *testing.T) {
	// Eight entries each: the chance of a random map order coming out sorted is
	// about 1 in 40320 per field per resolution, so 30 resolutions make an
	// accidental pass effectively impossible.
	excludeFrags := []string{"h", "g", "f", "e", "d", "c", "b", "a"}
	excludeMCP := []string{"zeta", "eta", "theta", "iota", "kappa", "lambda", "mu", "nu"}
	denyTools := []string{"Write", "Bash", "Edit", "WebFetch", "Task", "Read", "Glob", "Grep"}

	profiles := map[string]Profile{
		"p": {
			ExcludeFragments: excludeFrags,
			ExcludeMCP:       excludeMCP,
			DenyTools:        denyTools,
		},
	}

	// The fixture must actually be UNSORTED as authored, or "the output is
	// sorted" would prove nothing about the resolver.
	require.False(t, slices.IsSorted(excludeFrags), "the fixture's input order must not already be sorted")
	require.False(t, slices.IsSorted(denyTools), "the fixture's input order must not already be sorted")

	first, err := ResolveProfile(profiles, "p")
	require.NoError(t, err)
	require.Len(t, first.DenyTools, len(denyTools), "the resolver must carry every entry through")

	for i := 0; i < 30; i++ {
		resolved, err := ResolveProfile(profiles, "p")
		require.NoError(t, err)

		assert.True(t, slices.IsSorted(resolved.ExcludeFragments),
			"iteration %d: ExcludeFragments must have a stable order, got %v", i, resolved.ExcludeFragments)
		assert.True(t, slices.IsSorted(resolved.ExcludeMCP),
			"iteration %d: ExcludeMCP must have a stable order, got %v", i, resolved.ExcludeMCP)
		assert.True(t, slices.IsSorted(resolved.DenyTools),
			"iteration %d: DenyTools must have a stable order — it is written into engine settings files, got %v", i, resolved.DenyTools)

		assert.Equal(t, first.ExcludeFragments, resolved.ExcludeFragments, "iteration %d: unstable across resolutions", i)
		assert.Equal(t, first.ExcludeMCP, resolved.ExcludeMCP, "iteration %d: unstable across resolutions", i)
		assert.Equal(t, first.DenyTools, resolved.DenyTools, "iteration %d: unstable across resolutions", i)
	}
}
