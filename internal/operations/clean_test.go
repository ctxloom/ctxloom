package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// cleanFixture lays out one .ctxloom carrying a path from every tier, so the
// selection below is asserted against the real layout rather than a list this
// test agrees with itself about.
func cleanFixture(t *testing.T) (projectRoot, appDir string) {
	t.Helper()
	projectRoot = t.TempDir()
	appDir = filepath.Join(projectRoot, paths.AppDirName)

	write := func(rel, body string) {
		full := filepath.Join(projectRoot, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	// TierDerived, under cache/ — the selection.
	write(filepath.Join(paths.AppDirName, paths.CacheDir, paths.BundlesDir, "demo", "bundle.yaml"), "name: demo\n")
	write(filepath.Join(paths.AppDirName, paths.CacheDir, paths.ContextCacheDir, "abc.md"), "assembled\n")
	// TierDerived, but COMMITTED and outside cache/ — must survive.
	write(filepath.Join(paths.AppDirName, paths.LockFileName+".yaml"), "version: 1\n")
	// TierCommitted — the project's authored content.
	write(filepath.Join(paths.AppDirName, paths.ContentDir, "bundles", "mine.yaml"), "name: mine\n")
	// TierLocal — nothing rebuilds it.
	write(filepath.Join(paths.AppDirName, paths.StateDir, "local.txt"), "local\n")
	return projectRoot, appDir
}

// TestCleanCache_TakesTheCacheAndNothingElse pins the whole selection rule in
// one assertion set: what a clone can regenerate goes, what it cannot stays.
//
// lock.yaml is the entry that makes this more than "delete TierDerived". It IS
// derived — `ctxloom remote lock` rebuilds it — and it is committed anyway, so
// removing it would dirty the caller's tree while freeing nothing worth having.
func TestCleanCache_TakesTheCacheAndNothingElse(t *testing.T) {
	projectRoot, appDir := cleanFixture(t)

	res, err := CleanCache(appDir, t.TempDir(), true)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Positive(t, res.Bytes, "the plan must account for the bytes it reclaimed")

	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(projectRoot, rel))
		return err == nil
	}
	assert.False(t, exists(filepath.Join(paths.AppDirName, paths.CacheDir, paths.BundlesDir)),
		"the pulled bundle cache is regenerable by `ctxloom deps pull` and must go")
	assert.False(t, exists(filepath.Join(paths.AppDirName, paths.CacheDir, paths.ContextCacheDir)),
		"the assembled context is regenerable and must go")

	assert.True(t, exists(filepath.Join(paths.AppDirName, paths.LockFileName+".yaml")),
		"lock.yaml is derived but COMMITTED: removing it dirties the tree and frees nothing")
	assert.True(t, exists(filepath.Join(paths.AppDirName, paths.ContentDir, "bundles", "mine.yaml")),
		"authored content is the project's own and is never a clean target")
	assert.True(t, exists(filepath.Join(paths.AppDirName, paths.StateDir, "local.txt")),
		"TierLocal is what NOTHING rebuilds — clean means costs time, never costs history")
}

// TestCleanCache_WithoutApplyRemovesNothing pins the report-only default. The
// project's characteristic failure is a command that reports success having
// written nothing; this is that failure with the polarity reversed, and it is
// worse — the caller believes their cache survived and it did not.
func TestCleanCache_WithoutApplyRemovesNothing(t *testing.T) {
	projectRoot, appDir := cleanFixture(t)
	cached := filepath.Join(projectRoot, paths.AppDirName, paths.CacheDir, paths.BundlesDir, "demo", "bundle.yaml")

	res, err := CleanCache(appDir, t.TempDir(), false)
	require.NoError(t, err)
	assert.False(t, res.Applied)
	assert.Positive(t, res.Bytes, "a report still has to say what it would reclaim")
	for _, target := range res.Targets {
		assert.False(t, target.Removed, "%s must not be marked removed by a report", target.Rel)
	}

	_, statErr := os.Stat(cached)
	assert.NoError(t, statErr, "a report must leave every byte where it found it")
}

// TestCleanCache_EveryTargetNamesItsRebuild pins the property that makes the
// removal safe to offer at all: a caller is told, per path, the command that
// puts it back. An empty Rebuild would mean clean was about to delete
// something nothing reconstructs.
func TestCleanCache_EveryTargetNamesItsRebuild(t *testing.T) {
	_, appDir := cleanFixture(t)

	res, err := CleanCache(appDir, t.TempDir(), false)
	require.NoError(t, err)
	require.NotEmpty(t, res.Targets)
	for _, target := range res.Targets {
		assert.NotEmpty(t, target.Rebuild, "%s is a clean target with no way back", target.Rel)
	}
}
