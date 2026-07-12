package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
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
// production verification path — S4/S5) accepts as a valid publish
// signature, over the exact bundle file bytes.
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

	names := ListLocalBundleNames(cfg, nil)
	assert.Equal(t, []string{"alpha", "beta"}, names)
}

func TestListLocalBundleNames_EmptyWhenNoLocalDir(t *testing.T) {
	names := ListLocalBundleNames(&config.Config{}, nil)
	assert.Empty(t, names)
}
