package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// memBundleFS seeds an in-memory bundles dir with one bundle ("seed").
func memBundleFS(t *testing.T) (afero.Fs, *config.Config) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	bdir := authoredV1(appDir)
	require.NoError(t, fs.MkdirAll(bdir, 0755))
	bundletree.Write(t, fs, bdir, "seed", "version: 1.0.0\nfragments:\n  a:\n    content: hi\n")
	return fs, gatedFixture(config.Fixture{AppPaths: []string{appDir}})
}

func TestExportBundle_ToDestDir(t *testing.T) {
	fs, cfg := memBundleFS(t)

	res, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "seed"), res.Dest)

	exists, _ := afero.Exists(fs, filepath.Join(res.Dest, bundles.DirectoryFormManifest))
	assert.True(t, exists, "the exported tree should exist in the injected FS")
}

func TestExportBundle_RequiresDestination(t *testing.T) {
	fs, cfg := memBundleFS(t)

	_, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destination")
}

func TestImportBundle_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := gatedFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
	src := "/incoming/incoming"
	bundletree.Write(t, fs, "/incoming", "incoming", "version: 1.0.0\nfragments:\n  a:\n    content: hi\n")

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

// What lands is the SOURCE's bytes, not a re-emission of the parsed envelope.
// A publisher signature covers the files' exact bytes, so an import that
// round-tripped through the parser would drop comments and reorder keys and
// arrive unverifiable — while still reporting "imported".
//
// The fixture leads with a comment and a trailing key precisely because those
// are what a re-emission destroys; asserting on a body the parser would
// reproduce byte-for-byte would prove nothing.
func TestImportBundle_WritesTheSourceBytesVerbatim(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := gatedFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
	src := "/incoming/verbatim"
	body := "# a comment no re-emission keeps\nversion: 1.0.0\ndescription: last\n"
	testsupport.WriteFileString(t, fs, filepath.Join(src, bundles.DirectoryFormManifest), body, 0644)

	res, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, FS: fs})
	require.NoError(t, err)

	got, err := afero.ReadFile(fs, filepath.Join(res.Dest, bundles.DirectoryFormManifest))
	require.NoError(t, err)
	require.NotEmpty(t, got, "comparing two empty reads is trivially identical")
	assert.Equal(t, body, string(got))
}

func TestImportBundle_InvalidFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := gatedFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
	src := "/bad"
	require.NoError(t, afero.WriteFile(fs, filepath.Join(src, bundles.DirectoryFormManifest), []byte("\tnot: [valid"), 0644))

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
	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NoError(t, afero.WriteFile(fs, "/in/imported/bundle.yaml", []byte("version: 1.0.0\n"), 0644))

	res, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/in/imported", FS: fs})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(authoredV1(appDir), "imported"), res.Dest)
	inCache, _ := afero.Exists(fs, filepath.Join(paths.CacheBundlesPath(appDir), "imported"))
	assert.False(t, inCache, "imported bundle must not land in the gitignored cache")
}
