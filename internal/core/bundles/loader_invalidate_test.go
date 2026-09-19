package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoader_Invalidate_RederivesTheReaderSet pins what Invalidate MEANS: the
// next resolution re-reads every SOURCE, and the set of sources is itself
// derived from the world (a lockfile, a bundles directory) rather than fixed
// at construction.
//
// The defect this guards against: `deps pull` invalidated the shared loader
// after landing a new pinned bundle, and the loader dutifully re-resolved —
// the same pre-pull reader set, in which the new lockfile entry had never
// existed. The post-pull hooks step then reported the bundle it had just
// installed as "not found", and a second pull — a fresh process, a fresh
// reader set — was the only thing that made it load.
func TestLoader_Invalidate_RederivesTheReaderSet(t *testing.T) {
	alpha := &Bundle{Name: "alpha", Version: "1.0.0"}
	beta := &Bundle{Name: "beta", Version: "1.0.0"}

	// The world as the source sees it: alpha only, until "the pull lands".
	pulled := map[string]*Bundle{"alpha": alpha}
	source := func() []Reader { return []Reader{seedLocal(pulled)} }
	loader := NewLoaderFrom(source)

	_, err := loader.Load("alpha")
	require.NoError(t, err)
	_, err = loader.Load("beta")
	require.Error(t, err, "beta is not pinned yet; the pre-pull read must not see it")

	// The pull lands beta and announces it — exactly what
	// operations.SyncDependencies does after a batch.
	pulled["beta"] = beta
	loader.Invalidate()

	got, err := loader.Load("beta")
	require.NoError(t, err, "after Invalidate the loader must re-derive its sources, not merely re-read the old ones")
	assert.Equal(t, "beta", got.Name)
}

// TestNewLoader_FixedReadersSurviveInvalidate pins the other half: a loader
// built over an explicit reader list has no world to re-derive from, and
// Invalidate on it re-reads those same readers rather than dropping to none.
func TestNewLoader_FixedReadersSurviveInvalidate(t *testing.T) {
	loader := NewLoader(seedLocal(map[string]*Bundle{"alpha": {Name: "alpha", Version: "1.0.0"}}))

	_, err := loader.Load("alpha")
	require.NoError(t, err)

	loader.Invalidate()

	_, err = loader.Load("alpha")
	require.NoError(t, err, "a fixed reader set is the source; invalidating must not lose it")
}
