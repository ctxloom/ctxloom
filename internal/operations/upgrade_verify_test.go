package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// trustPublisher writes the project allowed_signers line that makes signer a
// key this machine trusts to PUBLISH. Without it, a stale signature is merely
// "unsigned to you" (signing.VerifyPublisher's quiet outcome) and the tamper
// case under test is never reached — the same trap the j001900 journey fell into.
func trustPublisher(t *testing.T, baseDir string, signer ssh.Signer) {
	t.Helper()
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	line := "publisher@example.com namespaces=\"" + signing.NamespacePublish + "\" " +
		string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "allowed_signers"), []byte(line), 0o644))
}

// signedBundleRepo builds a file:// source repo whose bundle is published as a
// complete signed pair — the bundle YAML and the detached `.sig` beside it —
// and returns the base dir, the ref, and the commit at which the pair verifies.
func signedBundleRepo(t *testing.T, body string) (baseDir, src, ref string, signer ssh.Signer, verified string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src = filepath.Join(tmp, "src")
	signer = testSigner(t)

	const bundlePath = ".ctxloom/content/bundles/demo.yaml"
	initLocalRepoWithFile(t, src, bundlePath, body)
	sig, err := signing.Sign([]byte(body), signer, signing.NamespacePublish)
	require.NoError(t, err)
	verified = addFileToLocalRepo(t, src, bundlePath+".sig", string(sig))

	trustPublisher(t, baseDir, signer)
	ref = "file://" + src + "@bundles/demo" // version-less → track the default branch
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")
	return baseDir, src, ref, signer, verified
}

// THE DECIDED BEHAVIOUR (taskloom unearned-cornea): a publisher edits a signed
// bundle and pushes without re-signing, so the newest commit carries bytes the
// signature beside them does not cover. `remote upgrade` must NOT move the pin
// onto it.
//
// What is at stake is not the new content — that was always going to be
// withheld as tampered. It is the OLD content: advancing the pin puts the last
// commit that actually verified out of reach, and the consumer is left with
// neither copy at exactly the moment a signature stopped verifying.
func TestUpgrade_RefusesAdvanceOntoUnverifiableSignature(t *testing.T) {
	baseDir, src, ref, _, verified := signedBundleRepo(t, "name: demo\n")
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	e0, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok)
	require.Equal(t, verified, e0.SHA, "the project starts pinned to the commit whose signature verifies")

	// The publisher edits and pushes; the stale .sig rides along unchanged.
	edited := addFileToLocalRepo(t, src, ".ctxloom/content/bundles/demo.yaml", "name: demo\nversion: \"2.0.0\"\n")
	require.NotEqual(t, verified, edited)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err, "a refused advance is a reported outcome, not a command failure")
	assert.Equal(t, 0, res.Advanced, "nothing may be counted as advanced")

	require.Len(t, res.Refused, 1, "the refusal must be REPORTED — a silent non-advance reads as 'already up to date'")
	assert.Equal(t, ref, res.Refused[0].Identity)
	assert.Equal(t, verified, res.Refused[0].KeptSHA)
	assert.Equal(t, edited, res.Refused[0].ProposedSHA)
	assert.Contains(t, res.Refused[0].Detail, "signature does not cover these bytes")

	// The payload assertion: the lockfile still holds the last verified pin,
	// whole. Nothing half-wrote.
	e1, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok, "the entry must still be there — refusing must not erase the pin")
	assert.Equal(t, verified, e1.SHA, "the pin stays at the last commit whose signature verified")
	assert.Equal(t, e0.URL, e1.URL)
	assert.Equal(t, e0.RequestedVersion, e1.RequestedVersion)
}

// The refusal must be NARROW. A publisher who re-signs properly is not
// penalised, and this is what keeps the guard honest: the same fixture, one
// extra commit carrying a fresh signature, and the pin moves.
func TestUpgrade_AdvancesOntoReSignedContent(t *testing.T) {
	baseDir, src, ref, signer, verified := signedBundleRepo(t, "name: demo\n")
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	const bundlePath = ".ctxloom/content/bundles/demo.yaml"
	revised := "name: demo\nversion: \"2.0.0\"\n"
	addFileToLocalRepo(t, src, bundlePath, revised)
	sig, err := signing.Sign([]byte(revised), signer, signing.NamespacePublish)
	require.NoError(t, err)
	reSigned := addFileToLocalRepo(t, src, bundlePath+".sig", string(sig))
	require.NotEqual(t, verified, reSigned)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Advanced, "a properly re-signed republish still advances")
	assert.Empty(t, res.Refused)

	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	assert.Equal(t, reSigned, e1.SHA)
}

// Unsigned content is NOT what this guard is about, and folding it in would be
// a different decision than the one taken. Unsigned content takes the review
// path — a human can act on it — so its advance still happens; only a signature
// that lies about its own bytes is refused.
func TestUpgrade_UnsignedContentStillAdvances(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")
	src := filepath.Join(tmp, "src")
	c1 := initLocalRepoWithFile(t, src, ".ctxloom/content/bundles/demo.yaml", "name: demo\n")
	ref := "file://" + src + "@bundles/demo"
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")

	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	c2 := addFileToLocalRepo(t, src, ".ctxloom/content/bundles/demo.yaml", "name: demo\nversion: \"2.0.0\"\n")
	require.NotEqual(t, c1, c2)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Advanced, "unsigned content is ordinary and still advances")
	assert.Empty(t, res.Refused)

	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	assert.Equal(t, c2, e1.SHA)
}
