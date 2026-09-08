package operations

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Authored bundles belong in the COMMITTED content tree, never the gitignored
// cache: `bundle create` writing to cache/bundles is how a project's own work
// ends up untracked by git (and invisible to `sign --all`).
func TestCreateBundle_WritesToCommittedContentTree(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	res, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "authored"})
	require.NoError(t, err)

	want := filepath.Join(authoredV1(appDir), "authored.yaml")
	assert.Equal(t, want, res.Path)
	assert.FileExists(t, want)

	_, err = os.Stat(filepath.Join(paths.CacheBundlesPath(appDir), "authored.yaml"))
	assert.True(t, os.IsNotExist(err), "authored bundle must not land in the gitignored cache")
}

// The publishing-repo acceptance case: a repo whose bundles live in
// .ctxloom/content/bundles/ must be enumerable by `ctxloom bundle sign --all`.
func TestListLocalBundleNames_FindsContentTreeBundles(t *testing.T) {
	// Real tempdir: GetBundleDirs os.Stat-gates on the real filesystem.
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	content := authoredV1(appDir)
	require.NoError(t, fs.MkdirAll(content, 0o755))
	for _, n := range []string{"alpha", "beta", "gamma"} {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(content, n+".yaml"),
			[]byte("version: 1.0.0\n"), 0o644))
	}
	// A stale cache artifact must not be offered for signing: this project has
	// no write authority over remote-pulled content.
	cacheDir := filepath.Join(paths.CacheBundlesPath(appDir), "github.com", "acme", "repo")
	require.NoError(t, fs.MkdirAll(cacheDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(cacheDir, "remote.yaml"),
		[]byte("version: 1.0.0\n_source:\n  sha: deadbeef\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names)
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

// A TREE bundle is ONE bundle, whatever it holds.
//
// Its items are .yaml documents indistinguishable by name from a single-file
// bundle, so a walk that descends into the tree offers each of them as a
// bundle of its own. That is not cosmetic: `ctxloom bundle sign --all` fed the
// enumeration straight into resolution and died with
//
//	Error: bundle "agent-ensemble/profiles/coordinator" not found
//
// which is a repo of converted bundles being unsignable. The assertion is an
// exact SET, not a length or a Contains: the failure ADDS names, so anything
// weaker passes with the boundary deleted.
func TestListLocalBundleNames_TreeBundleIsOneName(t *testing.T) {
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	testsupport.SeedTree(t, fs, authoredV2(appDir), map[string]string{
		"agent-ensemble/bundle.yaml":               "version: 1.0.0\n",
		"agent-ensemble/profiles/coordinator.yaml": "name: coordinator\n",
		"agent-ensemble/profiles/finder.yaml":      "name: finder\n",
		"agent-ensemble/fragments/delegation.md":   "# delegation\n",
	})
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-ensemble"}, names,
		"a tree bundle's profiles/*.yaml are its ITEMS; naming them as bundles is what makes `sign --all` die on a name that cannot resolve")
}

// Depth changes nothing: a skill package nests a directory INSIDE the tree, and
// a walk that survives one level of descent still fails at two.
func TestListLocalBundleNames_TreeBundleWithNestedItemsIsOneName(t *testing.T) {
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	testsupport.SeedTree(t, fs, authoredV2(appDir), map[string]string{
		"humanizer/bundle.yaml":               "version: 1.0.0\n",
		"humanizer/skills/humanize/SKILL.md":  "# humanize\n",
		"humanizer/skills/humanize/meta.yaml": "kind: skill\n",
		"humanizer/mcp/taskloom.yaml":         "command: taskloom\n",
	})
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)
	assert.Equal(t, []string{"humanizer"}, names,
		"nothing beneath a tree bundle is a bundle, at any depth")
}

// The boundary must stop the walk WITHOUT costing legitimate depth: a
// single-file bundle authored in a subdirectory is named by its path relative
// to the format root, and "personal/foo" is a name that resolves. A fix that
// simply refused to descend anywhere would delete that, silently.
func TestListLocalBundleNames_NestedSingleFileNamesSurvive(t *testing.T) {
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	testsupport.SeedTree(t, fs, authoredV1(appDir), map[string]string{
		"top.yaml":              "version: 1.0.0\n",
		"personal/foo.yaml":     "version: 1.0.0\n",
		"personal/lang/go.yaml": "version: 1.0.0\n",
	})
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)
	assert.Equal(t, []string{"personal/foo", "personal/lang/go", "top"}, names,
		"a single-file bundle at depth keeps its path-relative name")
}

// Both SHAPES in one project, at the same (only) format root: a bare document
// beside a true tree, which is the state a conversion actually leaves behind.
func TestListLocalBundleNames_MixedFormsEnumerateTogether(t *testing.T) {
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	testsupport.SeedTree(t, fs, authoredV1(appDir), map[string]string{
		"legacy.yaml": "version: 1.0.0\n",
	})
	testsupport.SeedTree(t, fs, authoredV2(appDir), map[string]string{
		"converted/bundle.yaml":         "version: 1.0.0\n",
		"converted/profiles/coder.yaml": "name: coder\n",
	})
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)
	assert.Equal(t, []string{"converted", "legacy"}, names)
}
