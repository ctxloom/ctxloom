package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportBundle_ToDestDir(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	destDir := t.TempDir()
	res, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: destDir})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(destDir, "seed.yaml"), res.Dest)

	_, err = os.Stat(res.Dest)
	require.NoError(t, err, "exported file should exist")
}

func TestExportBundle_RequiresDestination(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	_, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destination")
}

func TestImportBundle_RoundTrip(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// Author a bundle file outside the project, then import it.
	src := filepath.Join(t.TempDir(), "incoming.yaml")
	require.NoError(t, os.WriteFile(src, []byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))

	res, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src})
	require.NoError(t, err)
	assert.Equal(t, "imported", res.Status)
	assert.Equal(t, 1, res.Fragments)
	_, err = os.Stat(res.Dest)
	require.NoError(t, err)

	// Re-import without force fails; with force succeeds.
	_, err = ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, Force: true})
	require.NoError(t, err)
}

func TestImportBundle_InvalidFile(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	src := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(src, []byte("\tnot: [valid"), 0644))

	_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle file")
}
