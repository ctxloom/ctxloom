package operations

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Authored bundles belong in the COMMITTED content tree, never the gitignored
// cache: `bundle create` writing to cache/bundles is how a project's own work
// ends up untracked by git (and invisible to `sign --all`).
func TestCreateBundle_WritesToCommittedContentTree(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	res, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "authored"})
	require.NoError(t, err)

	want := filepath.Join(authoredV1(appDir), "authored", bundles.DirectoryFormManifest)
	assert.Equal(t, want, res.Path)
	assert.FileExists(t, want)

	_, err = os.Stat(filepath.Join(paths.CacheBundlesPath(appDir), "authored.yaml"))
	assert.True(t, os.IsNotExist(err), "authored bundle must not land in the gitignored cache")
}

// authoredV1 and authoredV2 both resolve to the SAME (only) format root now.
// paths.LocalBundlesPath is the bundles ROOT — the parent the format root is a
// child of — and the reader searches the format root, never the root itself: a
// fixture that writes straight to the bare root writes somewhere nothing
// looks, and the symptom is a bundle that resolves to nothing rather than an
// error anyone can read.
//
// The two names are kept SEPARATE, not collapsed into one, because they still
// name different SHAPES at the call site: authoredV1 is where a fixture writes
// a bare "<name>.yaml" document or a directory whose bundle.yaml still
// declares its items inline (treeFormEnvelope reads both as the old document
// form, unchanged); authoredV2 is where a fixture writes a true tree —
// "<name>/bundle.yaml" declaring nothing inline, with item files beside it.
// Renaming every one of this package's ~40 call sites to a single neutral name
// would erase that shape signal from the diff a reader actually needs.
func authoredV1(appPath string) string {
	return paths.LocalBundlesPathFor(appPath, paths.LayoutV2)
}

// repoV1 and repoV2 are authoredV1/authoredV2's REPO-relative counterparts —
// where a publishing repo commits a document/inline-directory versus a true
// tree. Both resolve to the same (only) format-v2 prefix now, for the same
// reason authoredV1/authoredV2 do; kept separate to preserve the shape signal
// at each call site.
func repoV1(rel ...string) string {
	return path.Join(append([]string{paths.RepoBundlesPrefixFor(paths.LayoutV2)}, rel...)...)
}

func repoV2(rel ...string) string {
	return path.Join(append([]string{paths.RepoBundlesPrefixFor(paths.LayoutV2)}, rel...)...)
}

func authoredV2(appPath string) string {
	return paths.LocalBundlesPathFor(appPath, paths.LayoutV2)
}
