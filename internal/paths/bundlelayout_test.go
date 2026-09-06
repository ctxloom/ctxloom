package paths

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayoutV1_AddsTheSameSegmentInAllThreePlaces is v1's half of the
// invariant LocalBundlesPath's doc states — a bundle repo and a consuming
// project lay their bundles out identically. v1 was relocated into its own
// format root, and splitting only some of the three accessors would make a
// consumer look somewhere the publisher does not write, with a bundle that
// resolves to nothing as the only symptom.
func TestLayoutV1_AddsTheSameSegmentInAllThreePlaces(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	assert.Equal(t, filepath.Join(LocalBundlesPath(app), "v1"), LocalBundlesPathFor(app, LayoutV1))
	assert.Equal(t, filepath.Join(CacheBundlesPath(app), "v1"), CacheBundlesPathFor(app, LayoutV1))
	assert.Equal(t, ".ctxloom/content/bundles/v1", RepoBundlesPrefixFor(LayoutV1))
}

// TestNoLayoutResolvesToTheBareBundlesRoot is what the relocation BOUGHT, and
// it is asserted over the layouts rather than over today's two so a third
// format cannot be added at the bare root.
//
// The bundles directory is the PARENT the format roots are siblings under; it
// holds no bundles itself. While one format still resolved to it, that format's
// walk saw every other format's subtree and named those bundles with a segment
// ("v2/atelier") — a name that resolves to nothing. Anything sitting at the
// bare root now belongs to no format and is deliberately invisible.
func TestNoLayoutResolvesToTheBareBundlesRoot(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	for _, l := range bundleLayouts() {
		t.Run(l.String(), func(t *testing.T) {
			assert.NotEqual(t, LocalBundlesPath(app), LocalBundlesPathFor(app, l))
			assert.NotEqual(t, CacheBundlesPath(app), CacheBundlesPathFor(app, l))
			assert.NotEqual(t, RepoBundlesRoot(), RepoBundlesPrefixFor(l))
			assert.NotEqual(t, ContentBundlesRoot(), ContentBundlesPrefixFor(l))
		})
	}
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
		_, err := l.Segment()
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
	v1, err := LayoutV1.Segment()
	require.NoError(t, err)
	v2, err := LayoutV2.Segment()
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
	_, err := LayoutUnknown.Segment()
	assert.True(t, errors.Is(err, ErrUnknownBundleLayout))
}
