package operations

import (
	"context"
	"os"
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

	want := filepath.Join(paths.LocalBundlesPath(appDir), "authored.yaml")
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
	content := paths.LocalBundlesPath(appDir)
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
