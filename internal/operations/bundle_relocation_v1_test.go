package operations

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestListLocalBundleNames_RelocatedLayoutsEnumerateUnderBareNames pins the
// enumeration half of the v1 relocation, which is a SEPARATE walk from the
// bundle reader's and therefore free to disagree with it.
//
// `sign --all` and `bundle list` reach their targets through
// ListLocalBundleNames, which walks the authored tree itself rather than asking
// the reader. GetBundleDirs hands it the bundles ROOT, and the bundles now live
// one directory further down — so a walk of the root names every bundle by its
// path relative to the ROOT and the layout segment rides into the name:
// "v1/alpha" instead of "alpha". That list looks entirely healthy (right
// length, plausible strings) and every name in it resolves to nothing, so
// `sign --all` signs nothing while reporting success — this project's
// characteristic failure, and the exact defect the content reorganisation was
// created to fix.
//
// The assertions are therefore on the NAMES, not merely on the count: a count
// alone passes against the broken enumeration.
func TestListLocalBundleNames_RelocatedLayoutsEnumerateUnderBareNames(t *testing.T) {
	// Real tempdir: GetBundleDirs os.Stat-gates on the real filesystem.
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")

	v1 := paths.LocalBundlesPathFor(appDir, paths.LayoutV1)
	v2 := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, fs.MkdirAll(v1, 0o755))
	require.NoError(t, fs.MkdirAll(filepath.Join(v2, "gamma"), 0o755))

	// v1 holds single-file documents; v2 holds tree form.
	for _, n := range []string{"alpha", "beta"} {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(v1, n+".yaml"),
			[]byte("version: 1.0.0\n"), 0o644))
	}
	require.NoError(t, afero.WriteFile(fs, filepath.Join(v2, "gamma", "bundle.yaml"),
		[]byte("version: 2.0.0\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)

	// Both layouts are enumerated, under the names a user actually types.
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names,
		"every relocated bundle must enumerate under its BARE name; a layout segment in the name is a name that resolves to nothing")

	// Said directly, because it is the defect's signature: the walk root is
	// the layout root, so no name may carry one.
	for _, n := range names {
		assert.NotContains(t, n, "v1/", "name %q carries its layout segment", n)
		assert.NotContains(t, n, "v2/", "name %q carries its layout segment", n)
	}

	// And the enumeration must agree with what the READER resolves. These are
	// two independent walks over one tree; when they disagree, `sign --all`
	// signs a set that is not the set anything can load.
	infos, err := bundles.NewLoader(bundles.NewProjectReader(fs, cfg.GetBundleDirs())).List()
	require.NoError(t, err)
	var loaded []string
	for _, b := range infos {
		loaded = append(loaded, b.Name)
	}
	sort.Strings(loaded)
	assert.Equal(t, loaded, names,
		"sign --all must enumerate exactly the bundles the loader can resolve")
}

// TestListLocalBundleNames_IgnoresFilesLooseAtTheBundlesRoot: after the
// relocation nothing is authored directly at the bundles root, and a stray file
// there is not a bundle. Enumerating it would reintroduce the very name the
// test above forbids, since the root is no longer any layout's search root.
func TestListLocalBundleNames_IgnoresFilesLooseAtTheBundlesRoot(t *testing.T) {
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")

	v1 := paths.LocalBundlesPathFor(appDir, paths.LayoutV1)
	require.NoError(t, fs.MkdirAll(v1, 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(v1, "real.yaml"),
		[]byte("version: 1.0.0\n"), 0o644))

	// Loose at the root, below no layout.
	require.NoError(t, afero.WriteFile(fs,
		filepath.Join(paths.LocalBundlesPath(appDir), "stray.yaml"),
		[]byte("version: 1.0.0\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)
	assert.Equal(t, []string{"real"}, names,
		"only bundles inside a layout root are authored bundles")
	for _, n := range names {
		assert.False(t, strings.Contains(n, "stray"), "a loose root file is not a bundle")
	}
}
