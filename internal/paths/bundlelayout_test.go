package paths

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayoutV1_IsTodaysPathUnchanged is the no-op half of introducing the
// layout accessors: nothing has moved yet, so v1 must resolve BYTE-IDENTICALLY
// to the three accessors that existed before. If this goes red while no
// relocation commit is in the diff, the layout surface has quietly moved every
// existing bundle out from under every path that names one.
func TestLayoutV1_IsTodaysPathUnchanged(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	assert.Equal(t, LocalBundlesPath(app), LocalBundlesPathFor(app, LayoutV1))
	assert.Equal(t, CacheBundlesPath(app), CacheBundlesPathFor(app, LayoutV1))
	assert.Equal(t, ".ctxloom/content/bundles", RepoBundlesPrefixFor(LayoutV1))
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
	assert.Equal(t, "/some/dir", BundlesLayoutRoot("/some/dir", LayoutV1))
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
