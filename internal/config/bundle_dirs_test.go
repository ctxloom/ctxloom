package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

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
