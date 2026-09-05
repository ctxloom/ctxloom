package paths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayoutV1_LocalHasRelocatedAndPublishedHasNot pins the ASYMMETRY that the
// local relocation deliberately introduced, on both sides at once.
//
// The local half must have MOVED: an authored v1 bundle now lives under v1/,
// and if this reverts to the bare bundles directory the reader walks a
// directory holding nothing while the bytes sit in v1/ — a bundle that resolves
// nowhere.
//
// The published and cache halves must NOT have moved: they name where an
// EXISTING bundle repo publishes and where a pull installs, and neither has
// been relocated. Flipping them renames the path every current consumer already
// fetches from. Both arms are asserted here so neither side can drift alone.
func TestLayoutV1_LocalHasRelocatedAndPublishedHasNot(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	// Local: relocated into its own directory, a sibling of v2.
	assert.Equal(t, filepath.Join(LocalBundlesPath(app), "v1"), LocalBundlesPathFor(app, LayoutV1))
	assert.NotEqual(t, LocalBundlesPath(app), LocalBundlesPathFor(app, LayoutV1))
	// Published and cache: unchanged, still the bare bundles root.
	assert.Equal(t, CacheBundlesPath(app), CacheBundlesPathFor(app, LayoutV1))
	assert.Equal(t, ".ctxloom/content/bundles", RepoBundlesPrefixFor(LayoutV1))
}

// TestLayoutV1_LocalAndPublishedAreSiblingsNotNested: v1/ and v2/ must be
// SIBLINGS in the local tree. If v1 ever resolved to the bundles root again,
// the v2 tree would sit INSIDE the v1 search root and every v2 bundle would
// also be found by the v1 walk under a name carrying the layout segment
// ("v2/unattended") — the second, wrong identity for one bundle that the
// reader dropped its exclusion logic on the strength of this being true.
func TestLayoutV1_LocalAndPublishedAreSiblingsNotNested(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	v1 := LocalBundlesPathFor(app, LayoutV1)
	v2 := LocalBundlesPathFor(app, LayoutV2)
	assert.NotEqual(t, v1, v2)
	assert.False(t, strings.HasPrefix(v2, v1+string(filepath.Separator)), "v2 %q must not nest inside v1 %q", v2, v1)
	assert.False(t, strings.HasPrefix(v1, v2+string(filepath.Separator)), "v1 %q must not nest inside v2 %q", v1, v2)
}

// TestLayoutV2_AddsTheSameSegmentInAllThreePlaces pins the invariant
// LocalBundlesPath's doc states — a bundle repo and a consuming project lay
// their bundles out identically — across the layout split. Splitting only one
// of the three makes a consumer look somewhere the publisher does not write,
// and the symptom is a bundle that resolves to nothing.
func TestLayoutV2_AddsTheSameSegmentInAllThreePlaces(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	assert.Equal(t, filepath.Join(LocalBundlesPath(app), "v2"), LocalBundlesPathFor(app, LayoutV2))
	assert.Equal(t, filepath.Join(CacheBundlesPath(app), "v2"), CacheBundlesPathFor(app, LayoutV2))
	assert.Equal(t, ".ctxloom/content/bundles/v2", RepoBundlesPrefixFor(LayoutV2))
}

// TestRepoBundlesPrefixFor_IsSlashSeparatedOnEveryOS: the repo prefix travels
// to a git host and is compared against forward-slash refs, so it must never
// pick up a separator from whatever OS is publishing.
func TestRepoBundlesPrefixFor_IsSlashSeparatedOnEveryOS(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, RepoBundlesPrefixFor(LayoutV2), "\\")
	assert.Contains(t, RepoBundlesPrefixFor(LayoutV2), "/v2")
}

// TestBundleLayout_UnknownIsRefusedNeverDefaulted. Defaulting an unrecognised
// layout to v1 is the silent wrong answer this type exists to prevent: it would
// hand back a real, existing directory for a question nobody could answer.
func TestBundleLayout_UnknownIsRefusedNeverDefaulted(t *testing.T) {
	t.Parallel()
	for _, l := range []BundleLayout{LayoutUnknown, BundleLayout(7), BundleLayout(-1)} {
		_, err := l.LocalSegment()
		require.Error(t, err, "layout %d", int(l))
		assert.ErrorIs(t, err, ErrUnknownBundleLayout)
		assert.Panics(t, func() { LocalBundlesPathFor("/project/.ctxloom", l) })
		assert.Panics(t, func() { CacheBundlesPathFor("/project/.ctxloom", l) })
		assert.Panics(t, func() { RepoBundlesPrefixFor(l) })
	}
}

// TestBundleLayout_SegmentsAreDistinct: v1 and v2 must not resolve to the same
// directory, or a migrated tree and the monolith it was migrated from would
// contest one name — the collision the separate paths exist to remove.
func TestBundleLayout_SegmentsAreDistinct(t *testing.T) {
	t.Parallel()
	v1, err := LayoutV1.LocalSegment()
	require.NoError(t, err)
	v2, err := LayoutV2.LocalSegment()
	require.NoError(t, err)
	assert.NotEqual(t, v1, v2)
	assert.NotEqual(t, LocalBundlesPathFor("/a/.ctxloom", LayoutV1), LocalBundlesPathFor("/a/.ctxloom", LayoutV2))
}

// TestBundlesLayoutRoot_TakesAnAlreadyResolvedRoot covers the accessor the
// bundle reader uses: it is handed search directories, never an appPath.
func TestBundlesLayoutRoot_TakesAnAlreadyResolvedRoot(t *testing.T) {
	t.Parallel()
	assert.Equal(t, filepath.Join("/some/dir", "v1"), BundlesLayoutRoot("/some/dir", LayoutV1))
	assert.Equal(t, filepath.Join("/some/dir", "v2"), BundlesLayoutRoot("/some/dir", LayoutV2))
}

func TestBundleLayout_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "v1", LayoutV1.String())
	assert.Equal(t, "v2", LayoutV2.String())
	assert.Equal(t, "unknown", LayoutUnknown.String())
	// The sentinel is reachable through the exported error, not only a panic.
	_, err := LayoutUnknown.LocalSegment()
	assert.True(t, errors.Is(err, ErrUnknownBundleLayout))
}
