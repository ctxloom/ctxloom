package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/testsupport"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// envelopeOnDisk decodes the bundle.yaml at path into its top-level keys.
func envelopeOnDisk(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &m))
	return m
}

func TestCreateBundle_StampsTheFormatGeneration(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	res, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "authored", Version: "1.2.0"})
	require.NoError(t, err)

	keys := envelopeOnDisk(t, res.Path)
	assert.Contains(t, keys, schemaver.Key)
	assert.Equal(t, "1.2.0", keys["version"], "the author's semver is written as given")
}

// Signing is the write that persists an older envelope: the manifest is built
// over the upgraded bytes, so the signed tree is current AND verifies.
func TestSignBundleFile_PersistsTheEnvelopeUpgradeBeforeHashing(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := filepath.Join(paths.BundlesLayoutRoot(cfg.GetBundleDirs()[0], paths.LayoutV2), "kit")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	envelope := filepath.Join(dir, bundles.DirectoryFormManifest)
	require.NoError(t, os.WriteFile(envelope, []byte("version: 1.0.0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte("KEEPER\n"), 0o644))
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{Target: SignTarget{BundleName: "kit"}, Signer: signer})
	require.NoError(t, err)

	keys := envelopeOnDisk(t, envelope)
	assert.Contains(t, keys, schemaver.Key)
	assert.Equal(t, "1.0.0", keys["version"])
	_, err = os.Stat(envelope + schemaver.BackupSuffix)
	assert.True(t, os.IsNotExist(err), "no backup is left inside a tree the manifest covers")

	verdict, verr := attest.VerifyBundle(context.Background(), openSignedTree(t, dir), signTrustRoot(signer), time.Now())
	require.NoError(t, verr)
	assert.True(t, verdict.OK(), "the upgraded tree is what was signed; got %q (%s)", verdict.Status, verdict.Detail)
	assert.NoError(t, verdict.Contents)
}

// Import copies a tree verbatim (its signature covers those bytes), so it
// never stamps; what it must do is refuse an envelope newer than this binary
// reads before anything lands.
func TestImportBundle_RefusesANewerEnvelope(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
	bundletree.Write(t, fs, "/incoming", "incoming", "version: 1.0.0\nfragments:\n  a:\n    content: hi\n")
	testsupport.WriteFile(t, fs, filepath.Join("/incoming", "incoming", bundles.DirectoryFormManifest),
		[]byte(schemaver.Key+": 99\nversion: 1.0.0\n"), 0o644)

	_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/incoming/incoming", FS: fs})

	require.ErrorIs(t, err, schemaver.ErrNewer)
	landed, existsErr := afero.Exists(fs, filepath.Join(authoredV1(filepath.Join("/proj", ".ctxloom")), "incoming"))
	require.NoError(t, existsErr)
	assert.False(t, landed)
}
