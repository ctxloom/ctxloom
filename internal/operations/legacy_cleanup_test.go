package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

func TestPurgeExtractedBundles(t *testing.T) {
	t.Run("nil cfg or no AppPaths is a no-op", func(t *testing.T) {
		removed, err := PurgeExtractedBundles(nil)
		require.NoError(t, err)
		assert.Zero(t, removed)

		removed, err = PurgeExtractedBundles(&config.Config{})
		require.NoError(t, err)
		assert.Zero(t, removed)
	})

	t.Run("missing bundles dir is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{AppPaths: []string{dir}}
		removed, err := PurgeExtractedBundles(cfg)
		require.NoError(t, err)
		assert.Zero(t, removed)
	})

	t.Run("removes only files with _source.sha", func(t *testing.T) {
		dir := t.TempDir()
		bundlesDir := paths.BundlesPath(dir)
		remoteDir := filepath.Join(bundlesDir, "remote-a")
		require.NoError(t, os.MkdirAll(remoteDir, 0o755))

		// One remote-pulled bundle (has _source.sha) — should be removed.
		pulled := filepath.Join(remoteDir, "pulled.yaml")
		require.NoError(t, os.WriteFile(pulled, []byte(`description: pulled
_source:
  sha: abc123
  url: https://example.com/foo
`), 0o644))

		// One locally-authored bundle (no _source) — should survive.
		local := filepath.Join(bundlesDir, "local.yaml")
		require.NoError(t, os.WriteFile(local, []byte(`description: local
fragments:
  hello:
    content: hi
`), 0o644))

		cfg := &config.Config{AppPaths: []string{dir}}
		removed, err := PurgeExtractedBundles(cfg)
		require.NoError(t, err)
		assert.Equal(t, 1, removed, "exactly the _source-tagged file should be removed")

		_, err = os.Stat(pulled)
		assert.True(t, os.IsNotExist(err), "pulled bundle should be gone")

		_, err = os.Stat(local)
		assert.NoError(t, err, "local bundle should still exist")

		// Now-empty remote dir should have been pruned.
		_, err = os.Stat(remoteDir)
		assert.True(t, os.IsNotExist(err), "empty per-remote dir should be pruned")
	})

	t.Run("preserves bundles dir even when emptied", func(t *testing.T) {
		dir := t.TempDir()
		bundlesDir := paths.BundlesPath(dir)
		require.NoError(t, os.MkdirAll(bundlesDir, 0o755))

		cfg := &config.Config{AppPaths: []string{dir}}
		removed, err := PurgeExtractedBundles(cfg)
		require.NoError(t, err)
		assert.Zero(t, removed)

		_, err = os.Stat(bundlesDir)
		assert.NoError(t, err, "root bundles dir should stay")
	})
}
