// Untagged, like the seam it pins: `just test` runs these with no build tag, so
// a seam that silently starts pointing somewhere else is caught by the fast
// gate rather than by 200-odd acceptance steps failing an hour later.
package acceptance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestBundlePathSeam_PinsTheRelocatedLayout is the whole warrant for the seam
// in bundle_paths.go.
//
// The corpus previously built these paths as hand-written string literals at
// ~60 sites, and an earlier attempt to relocate authored bundles into layout
// roots was REVERTED because those literals kept writing to a root nothing
// searched any more. The seam exists so that the relocation is one edit; this
// test is what makes that edit deliberate rather than silent.
//
// So the literals here are pinned ON PURPOSE. They are not a restatement of
// production for a reader's convenience — they are the ground truth of where
// the corpus writes its fixtures, and the seam is the thing under test. If this
// reddens, fixtures moved, and the question is whether production moved with
// them.
func TestBundlePathSeam_PinsTheRelocatedLayout(t *testing.T) {
	t.Parallel()

	const (
		singleFileRoot = ".ctxloom/content/bundles/v1"
		dirFormRoot    = ".ctxloom/content/bundles/v2"
	)

	assert.Equal(t, singleFileRoot, singleFileBundlesRoot(),
		"single-file bundles live in the v1 layout root")
	assert.Equal(t, dirFormRoot, dirFormBundlesRoot(),
		"directory-form bundles live in the v2 layout root")

	// Single-file form: <v1>/<name>.yaml
	assert.Equal(t, singleFileRoot+"/demo.yaml", bundleFilePath("demo"))
	assert.Equal(t, singleFileRoot+"/bundle-hookprobe.yaml", bundleFilePath("bundle-hookprobe"))

	// Directory form: <v2>/<name>, <v2>/<name>/bundle.yaml, and a file inside
	// the tree.
	assert.Equal(t, dirFormRoot+"/demo", bundleDirPath("demo"))
	assert.Equal(t, dirFormRoot+"/demo/bundle.yaml", bundleDirManifestPath("demo"))
	assert.Equal(t, dirFormRoot+"/demo/fragments/guidance.md",
		bundleDirItemPath("demo", "fragments/guidance.md"))
}

// TestBundlePathSeam_RootsAreSiblingsNeitherContainingTheOther pins the
// property every "scan the authored tree" step in the corpus depends on.
//
// It is a separate case from the literal pin above because it fails for a
// different reason and demands a different response. While one root CONTAINED
// the other — which is what the bare bundles directory did to v2 before the
// relocation — a scan of the containing root happened to see both forms, and a
// step that scanned only one still looked correct. Once they are siblings that
// stops being true silently: the scan goes green having examined half the tree.
// authoredBundlesRoots is the seam's answer, and this is what keeps it honest.
func TestBundlePathSeam_RootsAreSiblingsNeitherContainingTheOther(t *testing.T) {
	t.Parallel()

	single, dirForm := singleFileBundlesRoot(), dirFormBundlesRoot()

	assert.NotEqual(t, single, dirForm, "the two forms must resolve to different roots")
	assert.False(t, strings.HasPrefix(dirForm, single+"/"),
		"the directory-form root must not nest inside the single-file root")
	assert.False(t, strings.HasPrefix(single, dirForm+"/"),
		"the single-file root must not nest inside the directory-form root")

	assert.ElementsMatch(t, []string{single, dirForm}, authoredBundlesRoots(),
		"authoredBundlesRoots must name every root an authored bundle can sit in")

	// The seam derives from internal/paths rather than restating it, which is
	// what makes the relocation one edit instead of sixty.
	assert.Equal(t, paths.RepoBundlesPrefixFor(paths.LayoutV1), single)
	assert.Equal(t, paths.RepoBundlesPrefixFor(paths.LayoutV2), dirForm)
}
