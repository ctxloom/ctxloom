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
	bdir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bdir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "seed.yaml"),
		[]byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))
	return fs, config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
	src := "/bad.yaml"
	require.NoError(t, afero.WriteFile(fs, src, []byte("\tnot: [valid"), 0644))

	_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle file")
}

// --- committed-content layout + signature carry -----------------------------

// Export/import resolve the COMMITTED content tree, not the gitignored cache.
// memBundleFS seeds content/bundles, so a bundle it seeded being found at all
// is the assertion; this pins the write half.
func TestImportBundle_WritesToCommittedContentTree(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NoError(t, afero.WriteFile(fs, "/in/imported.yaml", []byte("version: 1.0.0\n"), 0644))

	res, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/in/imported.yaml", FS: fs})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(paths.LocalBundlesPath(appDir), "imported.yaml"), res.Dest)
	inCache, _ := afero.Exists(fs, filepath.Join(paths.CacheBundlesPath(appDir), "imported.yaml"))
	assert.False(t, inCache, "imported bundle must not land in the gitignored cache")
}

// A bundle's trust lives in its detached sibling .sig. Exporting the YAML alone
// silently strips it, so the copy arrives unsigned and unverifiable.
func TestExportBundle_CarriesDetachedSignature(t *testing.T) {
	fs, cfg := memBundleFS(t)
	src := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "seed.yaml")
	armored := signOnDisk(t, fs, src)

	res, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: "/out", FS: fs})
	require.NoError(t, err)

	assert.Equal(t, "/out/seed.yaml.sig", res.SigDest)
	got, err := afero.ReadFile(fs, "/out/seed.yaml.sig")
	require.NoError(t, err)
	assert.Equal(t, armored, got, "a verifying signature is carried byte-for-byte")
}

func TestExportBundle_NoSignature_NoSigWritten(t *testing.T) {
	fs, cfg := memBundleFS(t)

	res, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: "/out", FS: fs})
	require.NoError(t, err)

	assert.Empty(t, res.SigDest)
	exists, _ := afero.Exists(fs, "/out/seed.yaml.sig")
	assert.False(t, exists)
}

func TestImportBundle_CarriesDetachedSignature(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NoError(t, afero.WriteFile(fs, "/in/signed.yaml", []byte("version: 1.0.0\n"), 0644))
	require.NoError(t, afero.WriteFile(fs, "/in/signed.yaml.sig", []byte("-----BEGIN SSH SIGNATURE-----\nfake\n"), 0644))

	res, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/in/signed.yaml", FS: fs})
	require.NoError(t, err)

	wantSig := filepath.Join(paths.LocalBundlesPath(appDir), "signed.yaml.sig")
	assert.Equal(t, wantSig, res.SigDest)
	got, err := afero.ReadFile(fs, wantSig)
	require.NoError(t, err)
	assert.Contains(t, string(got), "BEGIN SSH SIGNATURE")
}
