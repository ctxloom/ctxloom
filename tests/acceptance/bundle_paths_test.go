// Untagged, like the seam it pins: `just test` runs these with no build tag, so
// a seam that silently starts pointing somewhere else is caught by the fast
// gate rather than by 200-odd acceptance steps failing an hour later.
package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestBundlePathSeam_PinsEachFamilysFormatRoot pins the literal path each
// fixture family is written to, per FORMAT.
//
// The corpus previously built these paths as hand-written string literals at
// ~60 sites, and routing them through the seam was only safe because the seam
// emitted byte-identical paths. The relocation has now landed, so the literals
// pinned here are the NEW ones — and they are pinned for the same reason as
// before: a seam that silently starts pointing somewhere else must redden the
// fast gate rather than 200-odd acceptance steps an hour later.
//
// PLACEMENT FOLLOWS FORMAT, NOT FILE SHAPE. The v1 root holds single-file
// documents and directories that still carry inline item keys; the v2 root
// holds true trees only.
func TestBundlePathSeam_PinsEachFamilysFormatRoot(t *testing.T) {
	t.Parallel()

	const v1Root = ".ctxloom/content/bundles/v1"
	const v2Root = ".ctxloom/content/bundles/v2"

	assert.Equal(t, v1Root, singleFileBundlesRoot(),
		"a single-file document is format v1")
	assert.Equal(t, v2Root, treeBundlesRoot(),
		"a true tree is format v2")

	// Single-file form: <v1>/<name>.yaml
	assert.Equal(t, v1Root+"/demo.yaml", bundleFilePath("demo"))
	assert.Equal(t, v1Root+"/bundle-hookprobe.yaml", bundleFilePath("bundle-hookprobe"))

	// True-tree form: <v2>/<name>, its envelope, and a file inside the tree.
	assert.Equal(t, v2Root+"/demo", treeBundlePath("demo"))
	assert.Equal(t, v2Root+"/demo/bundle.yaml", treeBundleManifestPath("demo"))
	assert.Equal(t, v2Root+"/demo/fragments/guidance.md",
		treeBundleItemPath("demo", "fragments/guidance.md"))

	// A DIRECTORY IS NOT A TREE: a bundle.yaml that still declares its items
	// inline is format v1, and it goes under the v1 root WITH the single files.
	assert.Equal(t, v1Root+"/demo", inlineDirBundlePath("demo"))
	assert.Equal(t, v1Root+"/demo/bundle.yaml", inlineDirBundleManifestPath("demo"))
}

// TestBundlePathSeam_FormatDecidesTheRootNotTheShape is the assertion the
// previous attempts at this relocation would have failed.
//
// The two DIRECTORY families must not share a root: one is a true tree and one
// is an inline-key directory, and filing the second under v2 because it happens
// to be a directory asserts a migration that never happened. The inline-key
// directory belongs with the single-file documents, because both are format v1.
func TestBundlePathSeam_FormatDecidesTheRootNotTheShape(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t, treeBundlesRoot(), singleFileBundlesRoot(),
		"the tree format and the document format are different roots")
	assert.Equal(t, singleFileBundlesRoot(), paths.RepoBundlesPrefixFor(inlineDirBundleLayout),
		"an inline-key directory is format v1, so it shares the v1 root with single-file documents")
	assert.Equal(t, paths.RepoBundlesPrefixFor(paths.LayoutV1), singleFileBundlesRoot(),
		"the seam must derive its root from internal/paths, not restate it")
}
