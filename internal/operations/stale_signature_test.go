package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// signOnDisk signs path's exact current bytes and writes the detached
// `<path>.sig` sibling — what `ctxloom sign` does. Returns the armored blob.
func signOnDisk(t *testing.T, fs afero.Fs, path string) []byte {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	armored, err := signing.Sign(data, testSigner(t), signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, path+".sig", armored, 0o644))
	return armored
}

// --- (a) invalidate at the cause --------------------------------------------

// THE REPRO, end to end through the real operations: sign a bundle, then add a
// fragment to it. The bytes change; the signature that covered the old bytes
// must not be left sitting beside the new ones — that pair is exactly what a
// consumer reads as tampering (trusted key, non-matching bytes → the bundle is
// withheld and the user is told to investigate an attack that never happened).
func TestAddItem_ToSignedBundle_InvalidatesTheSignature(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	sig, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "my-tools"},
		Signer: testSigner(t),
	})
	require.NoError(t, err)
	require.FileExists(t, sig.SigPath)

	_, err = AddItem(context.Background(), cfg, AddItemRequest{
		Bundle: "my-tools", Kind: ItemKindFragment, Name: "f", Content: "hello",
	})
	require.NoError(t, err)

	assert.NoFileExists(t, sig.SigPath,
		"editing a signed bundle must not leave its signature covering the OLD bytes")
}

// --- (b) defend at the boundary ---------------------------------------------

// Export must not copy a signature that does not cover the bytes it ships with:
// that is precisely the broken pair every consumer raises a tamper alarm on.
func TestExportBundle_StaleSignature_Refuses(t *testing.T) {
	fs, cfg := memBundleFS(t)
	src := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "seed.yaml")
	signOnDisk(t, fs, src)
	// The bundle changes after signing (a hand edit, or any writer that skipped
	// the store).
	require.NoError(t, afero.WriteFile(fs, src, []byte("version: 2.0.0\n"), 0644))

	_, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "seed", DestDir: "/out", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer covers")
	assert.Contains(t, err.Error(), "ctxloom bundle sign seed")

	sigExists, _ := afero.Exists(fs, "/out/seed.yaml.sig")
	assert.False(t, sigExists, "a stale signature must never be published")
	// And the refusal leaves NOTHING at the destination: exporting the YAML alone
	// would strip the bundle's trust silently, which is the same class of bug.
	bundleExists, _ := afero.Exists(fs, "/out/seed.yaml")
	assert.False(t, bundleExists, "a refused export must not leave a half-exported, trust-stripped copy")
}

// A move to a local path rides ExportBundle, so it inherits the refusal — and,
// critically, must leave the source untouched.
func TestMoveBundle_ToLocalPath_StaleSignature_RefusesAndKeepsSource(t *testing.T) {
	fs, cfg := memMoveFS(t, true)
	require.NoError(t, fs.MkdirAll("/out", 0755))
	src := srcBundlePath(cfg)
	require.NoError(t, afero.WriteFile(fs, src, []byte("version: 9.9.9\n"), 0644))

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer covers")

	exists, _ := afero.Exists(fs, src)
	assert.True(t, exists, "a refused move must leave the source in place")
}

// A move to a REMOTE carries the .sig verbatim into publish. A stale one must be
// refused before anything is published or the source removed.
func TestMoveBundle_ToRemote_StaleSignature_RefusesAndPublishesNothing(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "nope"}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)
	osfs := afero.NewOsFs()
	signOnDisk(t, osfs, bundlePath)
	require.NoError(t, os.WriteFile(bundlePath, []byte("version: 2.0.0\n"), 0644))

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{
		Name: "for-push", To: "personal", PublishManager: mgr,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer covers")

	assert.Empty(t, mock.createOrUpdateCalls, "nothing may be published")
	assert.FileExists(t, bundlePath, "a refused move must leave the source intact")
	assert.FileExists(t, bundlePath+".sig")
}

// Push is the last gate before bytes leave the machine: a carried signature that
// does not cover the bytes being published is refused outright.
func TestPushBundle_CarriedSignatureDoesNotCoverBytes_Refuses(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "nope"}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)

	armored, err := signing.Sign([]byte("some other bundle entirely\n"), testSigner(t), signing.NamespacePublish)
	require.NoError(t, err)

	_, err = PushBundle(context.Background(), cfg, PushBundleRequest{
		Path:           bundlePath,
		Remote:         "personal",
		PublishManager: mgr,
		Signature:      armored,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer covers")
	assert.Empty(t, mock.createOrUpdateCalls, "nothing may be published")
}

// A structurally broken .sig is a tamper signal too (spec §10.2) — never a
// silent downgrade to "publish it unsigned".
func TestPushBundle_UnparseableCarriedSignature_Refuses(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "nope"}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)

	_, err := PushBundle(context.Background(), cfg, PushBundleRequest{
		Path:           bundlePath,
		Remote:         "personal",
		PublishManager: mgr,
		Signature:      []byte("-----BEGIN SSH SIGNATURE-----\nnot-a-signature\n"),
	})
	require.Error(t, err)
	assert.Empty(t, mock.createOrUpdateCalls)
}

// The happy path must not regress: a signature that DOES cover the bytes is
// still carried verbatim (this is what makes a publish reproducible).
func TestPushBundle_ValidCarriedSignature_PublishedVerbatim(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "ok123"}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)
	armored := signOnDisk(t, afero.NewOsFs(), bundlePath)

	res, err := PushBundle(context.Background(), cfg, PushBundleRequest{
		Path:           bundlePath,
		Remote:         "personal",
		PublishManager: mgr,
		Signature:      armored,
	})
	require.NoError(t, err)
	assert.True(t, res.Signed)

	require.Len(t, mock.createOrUpdateCalls, 2, "bundle + .sig sibling")
	assert.Equal(t, armored, mock.createOrUpdateCalls[1].Content,
		"a verifying signature is carried byte-for-byte, never re-signed")
}
