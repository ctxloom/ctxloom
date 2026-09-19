package paths

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestBundlesLayoutRoot_TakesAnAlreadyResolvedRoot covers the accessor the
// bundle reader uses: it is handed search directories, never an appPath.
func TestBundlesLayoutRoot_TakesAnAlreadyResolvedRoot(t *testing.T) {
	t.Parallel()
	assert.Equal(t, filepath.Join("/some/dir", "v2"), BundlesLayoutRoot("/some/dir", LayoutV2))
}

func TestBundleLayout_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "v2", LayoutV2.String())
	assert.Equal(t, "unknown", LayoutUnknown.String())
	// The sentinel is reachable through the exported error, not only a panic.
	_, err := LayoutUnknown.Segment()
	assert.True(t, errors.Is(err, ErrUnknownBundleLayout))
}
