package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// memBundleFS seeds an in-memory bundles dir with one bundle ("seed").
func memBundleFS(t *testing.T) (afero.Fs, *config.Config) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	bdir := paths.BundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bdir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "seed.yaml"),
		[]byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))
	return fs, &config.Config{AppPaths: []string{appDir}}
}

func TestExportBundle_ToDestDir(t *testing.T) {
	fs, cfg := memBundleFS(t)

	res, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "seed.yaml"), res.Dest)

	exists, _ := afero.Exists(fs, res.Dest)
	assert.True(t, exists, "exported file should exist in the injected FS")
}

func TestExportBundle_RequiresDestination(t *testing.T) {
	fs, cfg := memBundleFS(t)

	_, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destination")
}

func TestImportBundle_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}}
	src := "/incoming/incoming.yaml"
	require.NoError(t, fs.MkdirAll("/incoming", 0755))
	require.NoError(t, afero.WriteFile(fs, src, []byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))

	res, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "imported", res.Status)
	assert.Equal(t, 1, res.Fragments)
	exists, _ := afero.Exists(fs, res.Dest)
	assert.True(t, exists)

	// Re-import without force fails; with force succeeds.
	_, err = ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, Force: true, FS: fs})
	require.NoError(t, err)
}

func TestImportBundle_InvalidFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}}
	src := "/bad.yaml"
	require.NoError(t, afero.WriteFile(fs, src, []byte("\tnot: [valid"), 0644))

	_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle file")
}
