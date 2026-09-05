// Untagged, like the seam it pins: `just test` runs these with no build tag, so
// a seam that silently starts pointing somewhere else is caught by the fast
// gate rather than by 200-odd acceptance steps failing an hour later.
package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestBundlePathSeam_ReproducesTheLiteralsItReplaced is the whole warrant for
// the refactor that introduced bundle_paths.go.
//
// The corpus previously built these paths as hand-written string literals at
// ~60 sites. Routing them through the seam is only safe if the seam emits
// BYTE-IDENTICAL paths — otherwise the refactor silently relocates every
// fixture in the suite, which is precisely the failure that got an earlier
// attempt at this reverted.
//
// So this pins the literals ON PURPOSE. They are not a restatement of
// production for a reader's convenience; they are the ground truth of what the
// corpus wrote before the seam existed, and the seam is the thing under test.
// When the relocation lands, THIS test is the one that must be edited
// deliberately — and its failure is the signal that fixtures moved.
func TestBundlePathSeam_ReproducesTheLiteralsItReplaced(t *testing.T) {
	t.Parallel()

	const literalRoot = ".ctxloom/content/bundles"

	assert.Equal(t, literalRoot, singleFileBundlesRoot(),
		"single-file bundles root must not move")
	assert.Equal(t, literalRoot, dirFormBundlesRoot(),
		"directory-form bundles root must not move")

	// Single-file form: <root>/<name>.yaml
	assert.Equal(t, literalRoot+"/demo.yaml", bundleFilePath("demo"))
	assert.Equal(t, literalRoot+"/bundle-hookprobe.yaml", bundleFilePath("bundle-hookprobe"))

	// Directory form: <root>/<name>, <root>/<name>/bundle.yaml, and a file
	// inside the tree.
	assert.Equal(t, literalRoot+"/demo", bundleDirPath("demo"))
	assert.Equal(t, literalRoot+"/demo/bundle.yaml", bundleDirManifestPath("demo"))
	assert.Equal(t, literalRoot+"/demo/fragments/guidance.md",
		bundleDirItemPath("demo", "fragments/guidance.md"))
}

// TestBundlePathSeam_BothFormsShareTodaysRoot pins the fact that makes this
// refactor green on arrival: the two forms are written into the SAME directory
// today, because the bare root is still the one the resolver searches.
//
// It is a separate case from the literal pin above because it fails for a
// different reason and demands a different response. Flipping
// dirFormBundleLayout to paths.LayoutV2 without moving the production reader in
// the same commit reddens THIS assertion, and the remedy is to move the reader
// — not to update the expectation.
func TestBundlePathSeam_BothFormsShareTodaysRoot(t *testing.T) {
	t.Parallel()

	assert.Equal(t, singleFileBundlesRoot(), dirFormBundlesRoot(),
		"both bundle forms must resolve to one root until the relocation lands")
	assert.Equal(t, paths.RepoBundlesPrefixFor(paths.LayoutV1), singleFileBundlesRoot(),
		"the seam must derive its root from internal/paths, not restate it")
}
