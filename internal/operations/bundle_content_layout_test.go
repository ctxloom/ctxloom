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

// authoredV1 is where a fixture must write a FORMAT-V1 authored bundle for this
// project's reader to find it.
//
// paths.LocalBundlesPath is the bundles ROOT — the parent every format root is
// a sibling under — and the reader searches the format roots, never the root
// itself. A fixture that writes straight to the root writes somewhere nothing
// looks, and the symptom is a bundle that resolves to nothing rather than an
// error anyone can read. The reader's own search DIRS still take the bare root:
// it does that expansion itself.
func authoredV1(appPath string) string {
	return paths.LocalBundlesPathFor(appPath, paths.LayoutV1)
}

// repoV1 is the repo-relative FORMAT ROOT a publishing repo commits format-v1
// bundles into. A fixture that serves or commits a bundle at the bare
// .ctxloom/content/bundles root serves it where no fetch looks; the bare root
// is only the parent a LISTING walks, which is why RepoItemRoot and these are
// different paths.
func repoV1(rel ...string) string {
	return path.Join(append([]string{paths.RepoBundlesPrefixFor(paths.LayoutV1)}, rel...)...)
}

// repoV2 is repoV1's counterpart for format v2 — exactly what
// remote.RepoItemPrefix resolves for a fetch and what a publish writes, now
// that the prefix has flipped. A fixture a real fetch or publish must reach
// belongs here, not under repoV1.
func repoV2(rel ...string) string {
	return path.Join(append([]string{paths.RepoBundlesPrefixFor(paths.LayoutV2)}, rel...)...)
}

// authoredV2 is where a fixture must write a FORMAT-V2 (tree) authored bundle,
// the counterpart of authoredV1 and for the same reason.
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

// Both forms in one project, which is the state a conversion actually leaves
// behind: the v1 root still holds documents while the v2 root holds trees.
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
