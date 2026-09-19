package operations

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// treeBundleFiles is the fixture a directory-form bundle is made of: the
// envelope, an item file one level down, the SHA256SUMS that covers them and a
// .sigs/ entry attesting it. Every one of them must survive a copy — the
// manifest and the .sigs/ store exist precisely to make a partial copy visible
// as tampering.
var treeBundleFiles = map[string]string{
	"bundle.yaml":                                    "version: 1.0.0\n",
	"skills/reviewer/SKILL.md":                       "# Reviewer\n\nreview the thing\n",
	"skills/reviewer/references/checklist.md":        "- one\n- two\n",
	"SHA256SUMS":                                     "aa11  bundle.yaml\nbb22  skills/reviewer/SKILL.md\n",
	".sigs/SHA256SUMS.publish.v1.ctxloom.dev.ab.sig": "-----BEGIN SSH SIGNATURE-----\nfixture\n-----END SSH SIGNATURE-----\n",
}

// writeTree materialises files (rel path -> content) under root.
func writeTree(t *testing.T, fs afero.Fs, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		target := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, fs.MkdirAll(filepath.Dir(target), 0755))
		require.NoError(t, iox.WriteFileAtomicFs(fs, target, []byte(body), 0644))
	}
}

// readTree returns every FILE under root keyed by its slash-separated path
// relative to root. Directories are not entries: what a bundle's SHA256SUMS
// covers is files, and an empty directory carries no bytes to compare.
func readTree(t *testing.T, fs afero.Fs, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	require.NoError(t, afero.Walk(fs, root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		data, readErr := afero.ReadFile(fs, p)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	}))
	return out
}

// assertTreesIdentical compares two trees file-for-file AND by count.
//
// The count is the assertion that matters: a comparison that only walks the
// files it found in the destination passes cheerfully when a whole subtree was
// dropped, because it never looks for what is not there. The zero-length guard
// is the other half — comparing two empty reads is trivially identical, so an
// all-empty fixture would make every byte assertion below vacuously true.
func assertTreesIdentical(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	require.NotEmpty(t, want, "fixture produced no files: every comparison below would be vacuous")
	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	gotNames := make([]string, 0, len(got))
	for name := range got {
		gotNames = append(gotNames, name)
	}
	sort.Strings(wantNames)
	sort.Strings(gotNames)
	require.Equal(t, wantNames, gotNames, "the copied tree does not hold exactly the source's files")
	require.Len(t, got, len(want), "exact file count")

	for _, name := range wantNames {
		require.NotZero(t, len(want[name]), "fixture file %s is empty; comparing two empty reads proves nothing", name)
		assert.Equal(t, want[name], got[name], "%s did not travel byte for byte", name)
	}
}

// memTreeBundleFS seeds a project holding one DIRECTORY-form bundle, "toolkit".
func memTreeBundleFS(t *testing.T) (afero.Fs, *config.Config, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	src := filepath.Join(authoredV1(appDir), "toolkit")
	writeTree(t, fs, src, treeBundleFiles)
	return fs, gatedFixture(config.Fixture{AppPaths: []string{appDir}}), src
}

// consumerConfig is a SECOND project on the same filesystem — the import side of
// the round trip, so the imported tree is compared against a source it did not
// overwrite.
func consumerConfig() *config.Config {
	return gatedFixture(config.Fixture{AppPaths: []string{filepath.Join("/consumer", ".ctxloom")}})
}

func TestExportImportBundleTree_RoundTripsEveryFileByteForByte(t *testing.T) {
	fs, cfg, src := memTreeBundleFS(t)
	want := readTree(t, fs, src)
	require.Len(t, want, len(treeBundleFiles), "fixture seeded the wrong number of files")

	exported, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "toolkit", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "toolkit"), exported.Dest,
		"a directory-form bundle exports under its own name, as a directory")
	assertTreesIdentical(t, want, readTree(t, fs, exported.Dest))

	imported, err := ImportBundle(context.Background(), consumerConfig(),
		ImportBundleRequest{SourcePath: exported.Dest, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "imported", imported.Status)
	assert.Equal(t, "1.0.0", imported.Version)
	assertTreesIdentical(t, want, readTree(t, fs, imported.Dest))
}

func TestImportBundleTree_LandsUnderItsOwnNameNotBundle(t *testing.T) {
	fs, cfg, _ := memTreeBundleFS(t)
	exported, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "toolkit", DestDir: "/out", FS: fs})
	require.NoError(t, err)

	consumer := consumerConfig()
	imported, err := ImportBundle(context.Background(), consumer, ImportBundleRequest{SourcePath: exported.Dest, FS: fs})
	require.NoError(t, err)

	// toolkit is a TRUE TREE, so the import files it under v2 — placement
	// follows the incoming document's FORMAT, not the fact that it is a
	// directory.
	consumerBundles := paths.LocalBundlesPathFor(consumer.GetAppPaths()[0], paths.LayoutV2)
	assert.Equal(t, filepath.Join(consumerBundles, "toolkit"), imported.Dest)

	// The old defect landed the manifest flat, renamed to "bundle" — a path
	// localFSReader skips, so it loaded as nothing at all.
	for _, wrong := range []string{"bundle.yaml", "bundle"} {
		present, statErr := afero.Exists(fs, filepath.Join(consumerBundles, wrong))
		require.NoError(t, statErr)
		assert.False(t, present, "%s must not exist: the tree was renamed instead of kept", wrong)
	}
}

func TestImportBundleTree_ManifestPathResolvesToTheWholeTree(t *testing.T) {
	fs, cfg, src := memTreeBundleFS(t)
	want := readTree(t, fs, src)
	exported, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "toolkit", DestDir: "/out", FS: fs})
	require.NoError(t, err)

	consumer := consumerConfig()
	imported, err := ImportBundle(context.Background(), consumer,
		ImportBundleRequest{SourcePath: filepath.Join(exported.Dest, "bundle.yaml"), FS: fs})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(paths.LocalBundlesPathFor(consumer.GetAppPaths()[0], paths.LayoutV2), "toolkit"), imported.Dest)
	assertTreesIdentical(t, want, readTree(t, fs, imported.Dest))
}

func TestImportBundleTree_ExistingTreeRefusedThenReplacedWholesale(t *testing.T) {
	fs, cfg, src := memTreeBundleFS(t)
	want := readTree(t, fs, src)
	exported, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "toolkit", DestDir: "/out", FS: fs})
	require.NoError(t, err)

	consumer := consumerConfig()
	imported, err := ImportBundle(context.Background(), consumer, ImportBundleRequest{SourcePath: exported.Dest, FS: fs})
	require.NoError(t, err)

	_, err = ImportBundle(context.Background(), consumer, ImportBundleRequest{SourcePath: exported.Dest, FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// A file the incoming version does not carry must not survive the replace:
	// left behind, it is content the incoming SHA256SUMS never covers.
	stale := filepath.Join(imported.Dest, "skills", "reviewer", "leftover.md")
	require.NoError(t, iox.WriteFileAtomicFs(fs, stale, []byte("dropped upstream\n"), 0644))

	forced, err := ImportBundle(context.Background(), consumer,
		ImportBundleRequest{SourcePath: exported.Dest, Force: true, FS: fs})
	require.NoError(t, err)
	assertTreesIdentical(t, want, readTree(t, fs, forced.Dest))
}

// TestExportBundle_SingleFileFormUnchanged pins the common path against the
// tree work: a single-file bundle still lands as one file named for the bundle.
func TestExportBundle_SingleFileFormUnchanged(t *testing.T) {
	fs, cfg := memBundleFS(t)
	want, err := afero.ReadFile(fs, filepath.Join(authoredV1(filepath.Join("/proj", ".ctxloom")), "seed.yaml"))
	require.NoError(t, err)
	require.NotZero(t, len(want), "fixture bundle is empty; the byte comparison below would prove nothing")

	res, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "seed.yaml"), res.Dest)

	isDir, err := afero.IsDir(fs, res.Dest)
	require.NoError(t, err)
	assert.False(t, isDir, "a single-file bundle must not become a directory")

	got, err := afero.ReadFile(fs, res.Dest)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assertTreesIdentical(t, map[string][]byte{"seed.yaml": want}, readTree(t, fs, "/out"))
}
