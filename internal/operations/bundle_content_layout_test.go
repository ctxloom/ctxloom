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
// bundles into — exactly what remote.RepoItemPrefix resolves for a fetch and
// what a publish writes. A fixture that serves or commits a bundle at the bare
// .ctxloom/content/bundles root serves it where no fetch looks; the bare root
// is only the parent a LISTING walks, which is why RepoItemRoot and these are
// different paths.
func repoV1(rel ...string) string {
	return path.Join(append([]string{paths.RepoBundlesPrefixFor(paths.LayoutV1)}, rel...)...)
}
