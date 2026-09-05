package paths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayoutV1_AddsTheSameSegmentInAllThreePlaces and its V2 twin pin the
// invariant LocalBundlesPath's doc states — a bundle repo and a consuming
// project lay their bundles out identically — across the layout split.
//
// Splitting only one of the three is the failure worth a test each: a consuming
// project then looks somewhere the publisher does not write, and the symptom is
// a bundle that resolves to NOTHING rather than an error anyone can read. An
// earlier revision moved the local half alone; these two cases are what make
// that state unrepresentable rather than merely discouraged.
func TestLayoutV1_AddsTheSameSegmentInAllThreePlaces(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	assert.Equal(t, filepath.Join(LocalBundlesPath(app), "v1"), LocalBundlesPathFor(app, LayoutV1))
	assert.Equal(t, filepath.Join(CacheBundlesPath(app), "v1"), CacheBundlesPathFor(app, LayoutV1))
	assert.Equal(t, ".ctxloom/content/bundles/v1", RepoBundlesPrefixFor(LayoutV1))

	// v1 has MOVED: if it resolves to the bare bundles root again, the reader
	// walks a directory holding nothing while the bytes sit in v1/.
	assert.NotEqual(t, LocalBundlesPath(app), LocalBundlesPathFor(app, LayoutV1))
	assert.NotEqual(t, CacheBundlesPath(app), CacheBundlesPathFor(app, LayoutV1))
}

func TestLayoutV2_AddsTheSameSegmentInAllThreePlaces(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	assert.Equal(t, filepath.Join(LocalBundlesPath(app), "v2"), LocalBundlesPathFor(app, LayoutV2))
	assert.Equal(t, filepath.Join(CacheBundlesPath(app), "v2"), CacheBundlesPathFor(app, LayoutV2))
	assert.Equal(t, ".ctxloom/content/bundles/v2", RepoBundlesPrefixFor(LayoutV2))
}

// TestBundleLayout_RootsAreSiblingsNotNested: v1/ and v2/ must be SIBLINGS in
// every root. If v1 ever resolved to the bundles root again, the v2 tree would
// sit INSIDE the v1 search root and every v2 bundle would ALSO be found by the
// v1 walk under a name carrying the layout segment ("v2/unattended") — a
// second, wrong identity for one bundle. bundles.localFSReader dropped its
// exclusion logic on the strength of this being true, so this is the assertion
// holding that deletion up.
func TestBundleLayout_RootsAreSiblingsNotNested(t *testing.T) {
	t.Parallel()
	const app = "/project/.ctxloom"
	for _, root := range []struct {
		name   string
		v1, v2 string
	}{
		{"local", LocalBundlesPathFor(app, LayoutV1), LocalBundlesPathFor(app, LayoutV2)},
		{"cache", CacheBundlesPathFor(app, LayoutV1), CacheBundlesPathFor(app, LayoutV2)},
		{"repo", RepoBundlesPrefixFor(LayoutV1), RepoBundlesPrefixFor(LayoutV2)},
	} {
		assert.NotEqual(t, root.v1, root.v2, "%s", root.name)
		assert.False(t, strings.HasPrefix(root.v2, root.v1+string(filepath.Separator)),
			"%s: v2 %q must not nest inside v1 %q", root.name, root.v2, root.v1)
		assert.False(t, strings.HasPrefix(root.v1, root.v2+string(filepath.Separator)),
			"%s: v1 %q must not nest inside v2 %q", root.name, root.v1, root.v2)
		// The repo prefix uses "/" on every OS, so check that separator too.
		assert.False(t, strings.HasPrefix(root.v2, root.v1+"/"), "%s", root.name)
		assert.False(t, strings.HasPrefix(root.v1, root.v2+"/"), "%s", root.name)
	}
}

// TestRepoBundlesPrefixFor_IsSlashSeparatedOnEveryOS: the repo prefix travels
// to a git host and is compared against forward-slash refs, so it must never
// pick up a separator from whatever OS is publishing.
func TestRepoBundlesPrefixFor_IsSlashSeparatedOnEveryOS(t *testing.T) {
	t.Parallel()
	for _, l := range []BundleLayout{LayoutV1, LayoutV2} {
		assert.NotContains(t, RepoBundlesPrefixFor(l), "\\", "layout %s", l)
		assert.Contains(t, RepoBundlesPrefixFor(l), "/"+l.String(), "layout %s", l)
	}
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
		assert.Panics(t, func() { BundlesLayoutRoot("/some/dir", l) })
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
	assert.NotEmpty(t, v1, "every layout contributes a real segment; an empty one puts its bundles loose at the root")
	assert.NotEmpty(t, v2)
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
