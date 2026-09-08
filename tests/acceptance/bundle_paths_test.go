// Untagged, like the seam it pins: `just test` runs these with no build tag, so
// a seam that silently starts pointing somewhere else is caught by the fast
// gate rather than by 200-odd acceptance steps failing an hour later.
package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestBundlePathSeam_PinsTheFormatRootAndEachShape pins the literal path each
// fixture family is written to.
//
// The corpus previously built these paths as hand-written string literals at
// ~60 sites, and routing them through the seam was only safe because the seam
// emitted byte-identical paths. Format v1 is gone, so there is exactly ONE
// format root left — the pin below is what would redden if the seam silently
// grew a second one, or silently stopped deriving from internal/paths.
func TestBundlePathSeam_PinsTheFormatRootAndEachShape(t *testing.T) {
	t.Parallel()

	const root = ".ctxloom/content/bundles/v2"

	assert.Equal(t, root, singleFileBundlesRoot(),
		"the single (only) format root")
	assert.Equal(t, root, treeBundlesRoot(),
		"the same root — format v2 is the only layout left")

	// Single-file form: <root>/<name>.yaml
	assert.Equal(t, root+"/demo.yaml", bundleFilePath("demo"))
	assert.Equal(t, root+"/bundle-hookprobe.yaml", bundleFilePath("bundle-hookprobe"))

	// True-tree form: <root>/<name>, its envelope, and a file inside the tree.
	assert.Equal(t, root+"/demo", treeBundlePath("demo"))
	assert.Equal(t, root+"/demo/bundle.yaml", treeBundleManifestPath("demo"))
	assert.Equal(t, root+"/demo/fragments/guidance.md",
		treeBundleItemPath("demo", "fragments/guidance.md"))

	// Inline-declaring directory form: same root, same shape as a true tree at
	// the PATH level — treeFormEnvelope tells the two apart by the envelope's
	// own content (whether it still declares items inline), never by path.
	assert.Equal(t, root+"/demo", inlineDirBundlePath("demo"))
	assert.Equal(t, root+"/demo/bundle.yaml", inlineDirBundleManifestPath("demo"))
}

// TestBundlePathSeam_EveryFamilyDerivesFromTheSameProductionRoot is the
// assertion that replaces what the previous (v1-holding) attempts at this
// relocation got wrong in two different ways: routing content into a SECOND
// root that does not exist, or filing a directory under the tree root purely
// because it is a directory (asserting a migration — inline items becoming
// real item files — that never happened for that fixture). With one format
// left, both failure modes collapse to the same check: every family's root
// must be the SAME expression, derived from internal/paths, never restated.
func TestBundlePathSeam_EveryFamilyDerivesFromTheSameProductionRoot(t *testing.T) {
	t.Parallel()

	want := paths.RepoBundlesPrefixFor(paths.LayoutV2)
	assert.Equal(t, want, singleFileBundlesRoot())
	assert.Equal(t, want, treeBundlesRoot())
	assert.Equal(t, want, paths.RepoBundlesPrefixFor(inlineDirBundleLayout))
	assert.Equal(t, want, paths.RepoBundlesPrefixFor(singleFileBundleLayout))
	assert.Equal(t, want, paths.RepoBundlesPrefixFor(treeBundleLayout))
}
