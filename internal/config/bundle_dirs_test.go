package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// writeBundle drops a minimal bundle YAML at path, creating parents.
func writeBundle(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestGetBundleDirs_ResolvesCommittedContentTree(t *testing.T) {
	appDir := t.TempDir()
	content := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(content, 0o755))

	cfg := &Config{appPaths: []string{appDir}}
	dirs := cfg.GetBundleDirs()

	require.Len(t, dirs, 1)
	assert.Equal(t, content, dirs[0])
	assert.Equal(t, filepath.Join(appDir, "content", "bundles"), dirs[0])
}

// The gitignored cache is never an authored-bundle directory: an existing
// cache/bundles must not put the cache on the authored read/write path.
func TestGetBundleDirs_ExcludesCacheBundles(t *testing.T) {
	appDir := t.TempDir()
	require.NoError(t, os.MkdirAll(paths.CacheBundlesPath(appDir), 0o755))

	cfg := &Config{appPaths: []string{appDir}}

	assert.Empty(t, cfg.GetBundleDirs())
}

func TestGetBundleDirs_NoContentDir(t *testing.T) {
	cfg := &Config{appPaths: []string{t.TempDir()}}
	assert.Empty(t, cfg.GetBundleDirs())
}

// A populated cache/bundles holding AUTHORED bundles (no _source) is work the
// layout move would otherwise hide: fatal finding, naming the dir and the move.
func TestGetBundleDirs_AuthoredBundlesInCache_RaisesFatalFinding(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() { strictness.Reset() })

	appDir := t.TempDir()
	writeBundle(t, filepath.Join(paths.CacheBundlesPath(appDir), "my-tools.yaml"), "version: \"1.0.0\"\n")
	require.NoError(t, os.MkdirAll(paths.LocalBundlesPath(appDir), 0o755))

	mark := strictness.Checkpoint()
	cfg := &Config{appPaths: []string{appDir}}
	_ = cfg.GetBundleDirs()

	found := strictness.Since(mark)
	require.Len(t, found, 1)
	assert.Equal(t, strictness.ClassMigration, found[0].Class)
	assert.Contains(t, found[0].Message, paths.CacheBundlesPath(appDir))
	assert.Contains(t, found[0].Message, "my-tools.yaml")
	assert.Contains(t, found[0].FixIt, paths.LocalBundlesPath(appDir))
}

// Legacy remote-pull artifacts (carrying _source.sha) are cache, not authored
// work: they are regenerable from the lockfile and clone cache, and must not
// fire the gate.
func TestGetBundleDirs_LegacyRemoteArtifactsInCache_NoFinding(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() { strictness.Reset() })

	appDir := t.TempDir()
	writeBundle(t,
		filepath.Join(paths.CacheBundlesPath(appDir), "github.com", "acme", "repo", "go.yaml"),
		"version: \"1.0.0\"\n_source:\n  sha: deadbeef\n")

	mark := strictness.Checkpoint()
	cfg := &Config{appPaths: []string{appDir}}
	_ = cfg.GetBundleDirs()

	assert.Empty(t, strictness.Since(mark))
}

// TestGetBundleDirs_AuthoredBundlesInCache_DegradedIsNotActionable pins what
// --degraded actually promises: it suppresses FATALITY, not RECORDING. The
// finding is still collected, so a degraded run can answer "what did you
// skip?"; what must be empty is the set the startup gate acts on.
func TestGetBundleDirs_AuthoredBundlesInCache_DegradedIsNotActionable(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(true)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})

	appDir := t.TempDir()
	writeBundle(t, filepath.Join(paths.CacheBundlesPath(appDir), "my-tools.yaml"), "version: \"1.0.0\"\n")

	mark := strictness.Checkpoint()
	cfg := &Config{appPaths: []string{appDir}}
	_ = cfg.GetBundleDirs()

	assert.Empty(t, strictness.Actionable(strictness.Since(mark)),
		"degraded mode still COLLECTS the finding; what must be empty is what the gate acts on")
	assert.NotEmpty(t, strictness.Since(mark),
		"the finding must still be RECORDED, or a degraded run cannot report what it skipped")
}
