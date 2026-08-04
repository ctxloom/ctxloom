package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/trust"
)

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer
}

// --- ResolveSignTarget ------------------------------------------------------

func TestResolveSignTarget_BareLocalName(t *testing.T) {
	target, err := ResolveSignTarget("my-tools")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", target.BundleName)
	assert.Empty(t, target.ItemNote)
}

func TestResolveSignTarget_ItemRefResolvesToContainingBundle(t *testing.T) {
	target, err := ResolveSignTarget("my-tools#fragments/go-testing")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", target.BundleName)
	assert.Equal(t, "fragments/go-testing", target.ItemNote)
}

func TestResolveSignTarget_LocalCanonicalRef(t *testing.T) {
	target, err := ResolveSignTarget("ctxloom:local@bundles/my-tools")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", target.BundleName)
}

func TestResolveSignTarget_RemoteRefRejected(t *testing.T) {
	_, err := ResolveSignTarget("https://github.com/ctxloom/ctxloom-default@bundles/go-tools#fragments/go-testing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not resolve to a bundle you author locally")
}

func TestResolveSignTarget_RemoteBundleOnlyRefRejected(t *testing.T) {
	_, err := ResolveSignTarget("https://github.com/ctxloom/ctxloom-default@bundles/go-tools")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not resolve to a bundle you author locally")
}

func TestResolveSignTarget_BuiltinRefRejected(t *testing.T) {
	_, err := ResolveSignTarget("builtin:ltk#fragments/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "builtin")
}

func TestResolveSignTarget_EmptyRefErrors(t *testing.T) {
	_, err := ResolveSignTarget("")
	require.Error(t, err)
}

// --- SignBundleFile -----------------------------------------------------

// TestSignBundleFile_WritesSigThatVerifyPublisherAccepts is the core
// red-line assertion: ctxloom sign writes a .sig VerifyPublisher (the real
// production verification path) accepts as a valid publish signature, over
// the exact bundle file bytes.
func TestSignBundleFile_WritesSigThatVerifyPublisherAccepts(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	signer := testSigner(t)

	res, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "my-tools"},
		Signer: signer,
	})
	require.NoError(t, err)
	assert.Equal(t, res.BundlePath+".sig", res.SigPath)

	bundleBytes, err := afero.ReadFile(afero.NewOsFs(), res.BundlePath)
	require.NoError(t, err)
	sigBytes, err := afero.ReadFile(afero.NewOsFs(), res.SigPath)
	require.NoError(t, err)
	require.NotEmpty(t, sigBytes)

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	principal, verr := signing.VerifyPublisher(bundleBytes, sigBytes, root, time.Now())
	require.NoError(t, verr)
	assert.Equal(t, "me@example.com", principal)
}

// TestSignBundleFile_TamperedBundleBytesFailVerification proves the
// signature is over the FILE bytes verbatim (spec §3.1): editing the bundle
// after signing must invalidate the signature, never verify anyway.
func TestSignBundleFile_TamperedBundleBytesFailVerification(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)
	signer := testSigner(t)

	res, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "my-tools"},
		Signer: signer,
	})
	require.NoError(t, err)

	sigBytes, err := afero.ReadFile(afero.NewOsFs(), res.SigPath)
	require.NoError(t, err)

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	tampered := []byte("version: \"9.9.9\"\n# tampered\n")
	_, verr := signing.VerifyPublisher(tampered, sigBytes, root, time.Now())
	require.Error(t, verr, "a signature over the original bytes must not verify over tampered bytes")
}

// TestSignBundleFile_NoSignerIsHardError is the "failing to sign is a hard
// error, never a silent unsigned publish" red line, at the operations
// layer: no signer supplied must never produce a bundle silently left
// without a .sig — it must error outright.
func TestSignBundleFile_NoSignerIsHardError(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	_, err = SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "my-tools"},
		Signer: nil,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signer")
}

func TestSignBundleFile_UnknownBundleErrors(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "does-not-exist"},
		Signer: signer,
	})
	require.Error(t, err)
}

// --- ListLocalBundleNames -------------------------------------------------

func TestListLocalBundleNames_ListsOnlyLocalBundles(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)
	_, err = CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "beta"})
	require.NoError(t, err)

	names, err := ListLocalBundleNames(cfg, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, names)
}

func TestListLocalBundleNames_EmptyWhenNoLocalDir(t *testing.T) {
	names, err := ListLocalBundleNames(&config.Config{}, nil)
	require.NoError(t, err)
	assert.Empty(t, names)
}

// --- SignItem (the Signable seam) ------------------------------------------

// TestSignItem_BundleRoundTripsThroughVerifyPublisher exercises the seam
// directly (bypassing SignBundleFile): a bundleSignable's PublisherPreimage
// is the bundle file's exact bytes, and SignItem must write a ".sig" sibling
// at bundle.Path + ".sig" that VerifyPublisher accepts over those exact
// bytes — the same red-line assertion TestSignBundleFile_... makes through
// the higher-level entry point, pinned here at the seam itself.
func TestSignItem_BundleRoundTripsThroughVerifyPublisher(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	store := bundleStore(cfg, nil)
	bundle, err := loadBundleForUpdate(store, cfg, "my-tools")
	require.NoError(t, err)

	fs := afero.NewOsFs()
	item := &bundleSignable{bundle: bundle, fs: fs}
	assert.Equal(t, trust.ItemKind("bundle"), item.Kind())
	assert.Equal(t, bundle.Path+".sig", item.SigPath())

	signer := testSigner(t)
	require.NoError(t, SignItem(fs, item, signer))

	bundleBytes, err := afero.ReadFile(fs, bundle.Path)
	require.NoError(t, err)
	sigBytes, err := afero.ReadFile(fs, item.SigPath())
	require.NoError(t, err)
	require.NotEmpty(t, sigBytes)

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	principal, verr := signing.VerifyPublisher(bundleBytes, sigBytes, root, time.Now())
	require.NoError(t, verr)
	assert.Equal(t, "me@example.com", principal)
}

func TestSignItem_NoSignerIsHardError(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	store := bundleStore(cfg, nil)
	bundle, err := loadBundleForUpdate(store, cfg, "my-tools")
	require.NoError(t, err)

	fs := afero.NewOsFs()
	item := &bundleSignable{bundle: bundle, fs: fs}
	err = SignItem(fs, item, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signer")

	_, statErr := fs.Stat(item.SigPath())
	assert.Error(t, statErr, "a refused sign must not leave a signature behind")
}

// SignBundleFile read the bundle file and handed the bytes to
// signing.Sign with no length check, so a truncated or zero-byte bundle got a
// .sig and a "Signed ..." line at exit 0 — a valid publish signature covering
// nothing. bundles.ParseBundle yaml.Unmarshals empty input without error, so the
// truncated file survives loadBundleForUpdate and reaches the signer.
func TestSignBundleFile_RefusesAZeroByteBundle(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "truncated"})
	require.NoError(t, err)

	fs := afero.NewOsFs()
	path := cfg.GetBundleDirs()[0] + "/truncated.yaml"
	require.NoError(t, afero.WriteFile(fs, path, nil, 0o644))

	_, err = SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "truncated"},
		Signer: testSigner(t),
		FS:     fs,
	})
	require.Error(t, err, "signing zero bytes must not report success")
	assert.Contains(t, err.Error(), "empty")

	_, statErr := fs.Stat(path + ".sig")
	assert.Error(t, statErr, "a refused sign must not leave a signature behind")
}

// ListLocalBundleNames swallowed EVERY ReadDir error with a bare
// `continue`, so a bundle dir it could not read was indistinguishable from one
// that simply is not there, and `sign --all` reported "no local bundles to
// sign" at exit 0. An absent dir is legitimately nothing; one that exists and
// cannot be read is a failure to find out.
//
// Note on reach: cfg.GetBundleDirs() already Stats each candidate and drops
// anything that is not a directory, so the surviving way to hit this is a
// directory whose contents cannot be listed -- mode 0000 here. Root ignores
// permission bits, so the test skips there rather than passing vacuously.
func TestListLocalBundleNames_UnreadableDirIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; the unreadable case cannot be staged")
	}
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)

	bundleDir := cfg.GetBundleDirs()[0]
	require.NoError(t, os.Chmod(bundleDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(bundleDir, 0o755) })

	_, err = ListLocalBundleNames(cfg, afero.NewOsFs())
	require.Error(t, err, "a bundle dir that cannot be read must not look like an empty one")
}

// The absent case stays green: a project with no authored bundle dir has
// legitimately nothing to list.
func TestListLocalBundleNames_AbsentDirIsNotAnError(t *testing.T) {
	names, err := ListLocalBundleNames(&config.Config{}, nil)
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestListLocalBundleNames_MatchesTheLoadersEnumeration is a gate, and it is
// deliberately a CROSS-CHECK against the real loader rather than a
// hand-written expectation: `sign --all` must sign every bundle the rest of
// ctxloom can load out of the authored dirs, and the only trustworthy
// statement of "every bundle" is the enumeration bundles.Loader actually
// performs. A hand-listed expectation would have gone stale the moment the
// loader learned a new bundle shape; this cannot.
//
// The defect it pins: ListLocalBundleNames dropped `e.IsDir()` outright, so
// every DIRECTORY-form bundle — i.e. exactly the bundles that can ship skills
// (skills.go requires directory form) — was unsignable via --all, and the
// command reported success having signed a subset.
func TestListLocalBundleNames_MatchesTheLoadersEnumeration(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := cfg.GetBundleDirs()[0]
	fs := afero.NewOsFs()

	// File-form bundle.
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)
	// Directory-form bundle — the shape that can carry skills.
	require.NoError(t, fs.MkdirAll(filepath.Join(dir, "gamma"), 0o755))
	require.NoError(t, fs.MkdirAll(filepath.Join(dir, "nested", "delta"), 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "gamma", "bundle.yaml"),
		[]byte("name: gamma\nversion: 0.1.0\n"), 0o644))
	// Nested directory-form bundle — the loader walks recursively, so --all
	// must reach this too.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "nested", "delta", "bundle.yaml"),
		[]byte("name: delta\nversion: 0.1.0\n"), 0o644))

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)

	infos, err := bundles.NewLoader(cfg.GetBundleDirs(), bundles.WithFS(fs)).List()
	require.NoError(t, err)
	var want []string
	for _, b := range infos {
		want = append(want, b.Name)
	}
	sort.Strings(want)

	assert.Equal(t, want, names,
		"sign --all must sign exactly the bundles the loader can load from the authored dirs")
	assert.Contains(t, names, "gamma", "a directory-form bundle must be signable via --all")

	// Widening the enumeration promotes a previously-theoretical question to
	// a live one: `sign --all` now HANDS these names to SignBundleFile, so
	// every name this returns must actually be signable. A list that names
	// bundles the signer then chokes on would just move the lie.
	signer := testSigner(t)
	for _, name := range names {
		res, serr := SignBundleFile(cfg, SignBundleRequest{
			Target: SignTarget{BundleName: name},
			Signer: signer,
			FS:     fs,
		})
		require.NoError(t, serr, "sign --all must be able to sign %q", name)
		ok, _ := afero.Exists(fs, res.SigPath)
		assert.True(t, ok, "no signature landed for %q at %s", name, res.SigPath)
	}
}

// --- directory-form bundles: signing the tree, not just its manifest ---------

// signDirBundle stages a DIRECTORY-form bundle carrying real content beside its
// manifest — the shape a publisher uses, and the only shape that can ship skills.
func signDirBundle(t *testing.T) (cfg *config.Config, dir string) {
	t.Helper()
	_, cfg = setupBundleTestDir(t)
	dir = filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "atelier")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundles.DirectoryFormManifest),
		[]byte("version: 1.0.0\ndescription: atelier\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "house-style.md"),
		[]byte("---\ndescription: d\n---\n\nHOUSE-STYLE-BODY\n"), 0o644))
	return cfg, dir
}

// signTrustRoot is a trust root that authorises this signer to publish.
func signTrustRoot(signer ssh.Signer) signing.TrustRoot {
	return allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
}

// openSignedTree reads the signed bundle back through the CONSUMER's surface —
// the same content.TreeStore a pulled bundle is read through.
func openSignedTree(t *testing.T, dir string) content.Bundle {
	t.Helper()
	store, err := content.NewTreeStore(afero.NewOsFs(), filepath.Dir(dir), content.Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := store.Open(context.Background(), content.BundleID(filepath.Base(dir)))
	require.NoError(t, err)
	return b
}

// THE WIRING ASSERTION. `ctxloom bundle sign` must produce an attestation the
// CONSUMER's verification path actually recognises.
//
// A directory-form bundle is verified by attest.VerifyBundle, which reads a
// SHA256SUMS manifest and the signatures filed against it. Signing only
// bundle.yaml's bytes writes a sibling .sig that path never looks at: the author
// sees exit 0 and a .sig on disk, and every consumer reads the bundle as
// UNATTESTED. That is a signature that attests nothing anyone checks.
func TestSignBundleFile_DirectoryFormProducesAnAttestationTheConsumerAccepts(t *testing.T) {
	cfg, dir := signDirBundle(t)
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier"},
		Signer: signer,
	})
	require.NoError(t, err)

	verdict, verr := attest.VerifyBundle(context.Background(), openSignedTree(t, dir), signTrustRoot(signer), time.Now())
	require.NoError(t, verr)
	assert.True(t, verdict.OK(),
		"a signed directory-form bundle must verify for a consumer; got status %q (%s)", verdict.Status, verdict.Detail)
	assert.Equal(t, "me@example.com", verdict.Principal)
	assert.NoError(t, verdict.Contents, "the signed manifest must cover the tree as published")
}

// The attestation must cover the CONTENT, not just the manifest file. Editing a
// fragment after signing has to break verification — otherwise the signature
// says nothing about the thing that reaches a model.
func TestSignBundleFile_DirectoryFormAttestationCoversContentNotJustTheManifest(t *testing.T) {
	cfg, dir := signDirBundle(t)
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier"},
		Signer: signer,
	})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "house-style.md"),
		[]byte("---\ndescription: d\n---\n\nSUBSTITUTED\n"), 0o644))

	verdict, verr := attest.VerifyBundle(context.Background(), openSignedTree(t, dir), signTrustRoot(signer), time.Now())
	require.NoError(t, verr)
	assert.False(t, verdict.OK(),
		"editing a fragment after signing must break the bundle's attestation")
}
