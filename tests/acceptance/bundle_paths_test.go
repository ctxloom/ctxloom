// Untagged, like the seam it pins: `just test` runs these with no build tag, so
// a seam that silently starts pointing somewhere else is caught by the fast
// gate rather than by 200-odd acceptance steps failing an hour later.
package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestBundlePathSeam_PinsTheFormatRoot pins the literal path each fixture is
// written to, and that it derives from internal/core/paths rather than
// restating it.
func TestBundlePathSeam_PinsTheFormatRoot(t *testing.T) {
	t.Parallel()

	const root = ".ctxloom/content/bundles/v2"
	assert.Equal(t, root, paths.RepoBundlesPrefixFor(paths.LayoutV2))

	assert.Equal(t, root+"/demo/bundle.yaml", bundleFilePath("demo"))
	assert.Equal(t, root+"/demo", treeBundlePath("demo"))
	assert.Equal(t, root+"/demo/bundle.yaml", treeBundleManifestPath("demo"))
	assert.Equal(t, root+"/demo/fragments/guidance.md",
		treeBundleItemPath("demo", "fragments/guidance.md"))
}
