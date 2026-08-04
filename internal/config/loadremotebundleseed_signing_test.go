package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// signedSeedRepo builds a git repo shipping `secure.yaml` with a detached
// `secure.yaml.sig`, whose bytes are produced by signFn over the bundle's file
// bytes. signFn lets a test ship a VALID signature, a signature over DIFFERENT
// bytes (tamper), or garbage. It returns the repo path, HEAD SHA, the exact
// bundle bytes, and the signer's public key line for an allowed_signers file.
func signedSeedRepo(t *testing.T, signFn func(bundleBytes []byte, signer ssh.Signer) []byte) (repoDir, sha string, pubLine string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	pubLine = string(ssh.MarshalAuthorizedKey(sshPub))

	repoDir = filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(repoDir, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	bundleBytes := []byte("version: v1\ndescription: a signed bundle\n")
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "secure.yaml"), bundleBytes, 0o644))

	files := []string{".ctxloom/content/bundles/secure.yaml"}
	if signFn != nil {
		sig := signFn(bundleBytes, sshSigner)
		if sig != nil {
			require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "secure.yaml.sig"), sig, 0o644))
			files = append(files, ".ctxloom/content/bundles/secure.yaml.sig")
		}
	}
	for _, f := range files {
		_, err = wt.Add(f)
		require.NoError(t, err)
	}
	commit, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t.com", When: time.Now()},
	})
	require.NoError(t, err)
	return repoDir, commit.String(), pubLine
}

// seedWith drives loadRemoteBundleSeed end to end over a real clone of repoDir,
// with the given allowed_signers content committed at appDir. Returns the seed.
func seedWith(t *testing.T, repoDir, sha, allowedSigners string) map[string]*bundleForTest {
	t.Helper()
	repoURL := "file://" + repoDir

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	if allowedSigners != "" {
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "allowed_signers"), []byte(allowedSigners), 0o644))
	}

	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	lock.AddEntry(remote.ItemTypeBundle, repoURL+"@bundles/secure",
		remote.LockEntry{SHA: sha, URL: repoURL, FetchedAt: time.Now().UTC()})
	require.NoError(t, lm.Save(lock))

	cfg := &Config{appPaths: []string{appDir}}
	raw := remoteBundleSeed(t, cfg)
	out := map[string]*bundleForTest{}
	for k, b := range raw {
		out[k] = &bundleForTest{signer: b.Signer(), version: b.Version}
	}
	return out
}

type bundleForTest struct {
	signer  string
	version string
}

// TestSeed_VerifiedPublisherStampsSigner is the happy path: a bundle signed by a
// key in allowed_signers, over its exact bytes, is stamped with the publisher
// principal — which allows it at step 4 with no review.
func TestSeed_VerifiedPublisherStampsSigner(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha, pubLine := signedSeedRepo(t, func(b []byte, s ssh.Signer) []byte {
		sig, err := signing.Sign(b, s, signing.NamespacePublish)
		require.NoError(t, err)
		return sig
	})
	allowed := "context@acme.com namespaces=\"" + signing.NamespacePublish + "\" " + pubLine

	seed := seedWith(t, repoDir, sha, allowed)
	canonical := "file://" + repoDir + "@bundles/secure"
	b, ok := seed[canonical]
	require.True(t, ok, "the signed bundle is loaded")
	assert.Equal(t, "context@acme.com", b.signer,
		"a bundle signed by a trusted publisher key, over its exact bytes, is stamped with that principal")
}

// TestSeed_UnsignedBundleTakesReviewPath proves an unsigned bundle (no .sig) is
// loaded normally with an EMPTY signer — legal, ordinary, and destined for the
// review path (step 6). No key is required to consume it.
func TestSeed_UnsignedBundleTakesReviewPath(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha, _ := signedSeedRepo(t, nil) // no .sig at all

	seed := seedWith(t, repoDir, sha, "") // no allowed_signers either
	canonical := "file://" + repoDir + "@bundles/secure"
	b, ok := seed[canonical]
	require.True(t, ok, "an unsigned bundle still loads — unsigned is legal")
	assert.Equal(t, "v1", b.version, "its content parses normally")
	assert.Empty(t, b.signer, "an unsigned bundle carries no signer and takes the review path")
}

// TestSeed_UntrustedKeyIsUnsigned proves a validly-signed bundle whose key is
// NOT in allowed_signers is unsigned-to-us: it loads with an empty signer and
// takes the review path, quietly (no error, no tamper signal).
func TestSeed_UntrustedKeyIsUnsigned(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha, _ := signedSeedRepo(t, func(b []byte, s ssh.Signer) []byte {
		sig, err := signing.Sign(b, s, signing.NamespacePublish)
		require.NoError(t, err)
		return sig
	})

	// Empty allowed_signers → the signing key is unknown to us.
	seed := seedWith(t, repoDir, sha, "")
	canonical := "file://" + repoDir + "@bundles/secure"
	b, ok := seed[canonical]
	require.True(t, ok, "a bundle signed by an untrusted key still loads")
	assert.Empty(t, b.signer, "a signature by an untrusted key is unsigned content to us")
}

// TestSeed_TamperedSignatureWithholdsBundle is IMPLEMENTER TRAP #1 end to end:
// a .sig present at the correct sibling path, by a TRUSTED key, but NOT over
// these bytes, must NOT downgrade to unsigned/pending — the whole bundle is
// withheld (absent from the seed). Corrupting a signature must never launder a
// signed bundle into an unsigned one.
func TestSeed_TamperedSignatureWithholdsBundle(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha, pubLine := signedSeedRepo(t, func(_ []byte, s ssh.Signer) []byte {
		// Sign DIFFERENT bytes than the bundle actually contains — a valid
		// signature by the trusted key, but not over the content it sits beside.
		sig, err := signing.Sign([]byte("these are not the bundle's bytes"), s, signing.NamespacePublish)
		require.NoError(t, err)
		return sig
	})
	allowed := "context@acme.com namespaces=\"" + signing.NamespacePublish + "\" " + pubLine

	seed := seedWith(t, repoDir, sha, allowed)
	canonical := "file://" + repoDir + "@bundles/secure"
	_, ok := seed[canonical]
	assert.False(t, ok,
		"a trusted key's signature that does not cover these bytes is TAMPER — the bundle is withheld entirely, never degraded to unsigned")
}

// TestSeed_CorruptedSignatureBodyWithholdsBundle is the trap-1 variant the brief
// names explicitly: correct filename, corrupted signature BODY ⇒ the bundle is
// withheld (tamper), not admitted as pending/unsigned.
func TestSeed_CorruptedSignatureBodyWithholdsBundle(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha, pubLine := signedSeedRepo(t, func(b []byte, s ssh.Signer) []byte {
		sig, err := signing.Sign(b, s, signing.NamespacePublish)
		require.NoError(t, err)
		// Flip bytes in the middle of the armored blob: still shaped like a
		// signature, no longer a valid one.
		for i := len(sig) / 3; i < len(sig)/3+8 && i < len(sig); i++ {
			sig[i] ^= 0xff
		}
		return sig
	})
	allowed := "context@acme.com namespaces=\"" + signing.NamespacePublish + "\" " + pubLine

	seed := seedWith(t, repoDir, sha, allowed)
	canonical := "file://" + repoDir + "@bundles/secure"
	_, ok := seed[canonical]
	assert.False(t, ok, "a corrupted signature body over a trusted key is tamper — the bundle is withheld")
}
