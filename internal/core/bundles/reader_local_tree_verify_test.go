package bundles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A locally authored TREE keeps its payload in files beside its envelope, so
// the sibling `bundle.yaml.sig` covers a document that declares nothing. Before
// this, mutating an item file left the bundle reporting SignatureValid while
// the SHA256SUMS manifest that would have caught it sat on disk unread.
//
// EVERY TEST HERE GOES THROUGH THE READER, never through attest.VerifyBundle
// directly. Calling the verifier would prove the verifier works — which was
// never in doubt — and say nothing about whether the read path consults it.
//
// AND EVERY MUTATION IS TO AN ITEM FILE, never to bundle.yaml. A test that
// mutates the envelope passes against the pre-change reader and therefore
// proves nothing at all.

const verifyTreeName = "vault"

// stageSignedTree writes a tree-form bundle and signs it through its ONE
// signature: the SHA256SUMS manifest and its .sigs/ entry (attest.SignBundle).
// It returns the filesystem, the bundle directory, and a trust root that
// trusts the signing key, so a caller can assert a TRUSTED verdict rather
// than merely a well-formed one.
func stageSignedTree(t *testing.T, root, fragBody string) (afero.Fs, string, signing.TrustRoot) {
	t.Helper()
	fsys, dir := stageUnsignedTree(t, root, fragBody)
	signer, pub := testSkillSigner(t)
	signTreeManifest(t, fsys, root, signer)
	return fsys, dir, skillPublisherRoot("publisher@example.test", pub)
}

// stageUnsignedTree writes the tree itself, with no manifest and no signatures.
func stageUnsignedTree(t *testing.T, root, fragBody string) (afero.Fs, string) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	v2 := paths.BundlesLayoutRoot(root, paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2, 0o755))
	st, err := content.NewTreeStore(fsys, v2, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		trust.Ref{Bundle: verifyTreeName, Kind: trust.KindFragment, Name: "house-style"},
		signing.FormRaw,
		content.Fragment{Name: "house-style", ItemMeta: content.ItemMeta{Body: fragBody}}))
	require.NoError(t, st.PutRootFile(context.Background(), verifyTreeName, DirectoryFormManifest,
		[]byte("name: "+verifyTreeName+"\nversion: 2.0.0\n")))
	return fsys, filepath.Join(v2, verifyTreeName)
}

// signTreeManifest writes and signs SHA256SUMS over the tree as it stands.
func signTreeManifest(t *testing.T, fsys afero.Fs, root string, signer ssh.Signer) {
	t.Helper()
	v2 := paths.BundlesLayoutRoot(root, paths.LayoutV2)
	st, err := content.NewTreeStore(fsys, v2, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := st.Open(context.Background(), verifyTreeName)
	require.NoError(t, err)
	require.NoError(t, attest.SignBundle(context.Background(), st, b, signer))
}

// mutateAnItemFile appends a byte to the first file inside the tree's fragments
// directory, leaving bundle.yaml and its sibling signature untouched. It
// REQUIRES that it found one: a mutation helper that silently mutated nothing
// would make every assertion below vacuous.
func mutateAnItemFile(t *testing.T, fsys afero.Fs, dir string) string {
	t.Helper()
	var target string
	require.NoError(t, afero.Walk(fsys, dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || target != "" {
			return err
		}
		if strings.Contains(filepath.ToSlash(p), "/"+string(trust.KindFragment)+"/") ||
			strings.Contains(filepath.ToSlash(p), "/fragments/") {
			target = p
		}
		return nil
	}))
	require.NotEmpty(t, target, "no item file found under %s — the fixture wrote no payload, so nothing below would be testing anything", dir)

	before, err := afero.ReadFile(fsys, target)
	require.NoError(t, err)
	testsupport.WriteFileString(t, fsys, target, string(before)+"\nTAMPERED\n", 0o644)

	after, err := afero.ReadFile(fsys, target)
	require.NoError(t, err)
	require.NotEqual(t, before, after, "the mutation did not change the file")
	return target
}

// readTree reads the staged bundle through the PROJECT READER — the production
// local read path — and returns its read.
func readTree(t *testing.T, fsys afero.Fs, root string, trustRoot signing.TrustRoot) BundleRead {
	t.Helper()
	var opts []ReaderOption
	if trustRoot != nil {
		opts = append(opts, WithTrustRoot(trustRoot))
	}
	reads, err := NewProjectReader(fsys, []string{root}, opts...).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)
	return reads[0]
}

// TestLocalTree_SignedAndIntact_ReadsValid is the BASELINE the mutation test
// needs. Without it, a reader that reported invalid for every tree would pass
// the mutation test while being completely broken.
func TestLocalTree_SignedAndIntact_ReadsValid(t *testing.T) {
	fsys, _, root := stageSignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	read := readTree(t, fsys, "/bundles", root)

	assert.Equal(t, SignatureValid, read.Signature(), "an intact, signed tree must read as valid")
	assert.Equal(t, SignerTrusted, read.Signer())
	require.Equal(t, "FRAG-BODY-MARKER", read.Bundle.Fragments["house-style"].Content)
}

// TestLocalTree_ItemFileMutated_LocalReaderReportsInvalid is the finding this
// change exists to close. The envelope and its sibling signature are untouched
// and still verify, so the pre-change reader reported SignatureValid here.
func TestLocalTree_ItemFileMutated_LocalReaderReportsInvalid(t *testing.T) {
	fsys, dir, root := stageSignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	mutated := mutateAnItemFile(t, fsys, dir)

	// The envelope pair is provably untouched: whatever the reader now reports,
	// it cannot be reporting on bundle.yaml.
	envelope, err := afero.ReadFile(fsys, filepath.Join(dir, DirectoryFormManifest))
	require.NoError(t, err)
	require.Equal(t, "name: "+verifyTreeName+"\nversion: 2.0.0\n", string(envelope))

	read := readTree(t, fsys, "/bundles", root)
	assert.Equal(t, SignatureInvalid, read.Signature(),
		"an edited item file (%s) must invalidate the tree, not ride in under the envelope's signature", mutated)
	assert.Contains(t, read.SignatureDetail(), content.ManifestPath,
		"the diagnostic must name the manifest, so the remedy is re-signing the tree")
}

// TestLocalTree_ItemFileMutated_ContentIsStillDelivered. Local content is
// trusted BY LOCALITY, so these facts are a DIAGNOSTIC and must never withhold
// an author's own bundle — a check that silently emptied the local tree would
// be a worse bug than the one it fixed.
func TestLocalTree_ItemFileMutated_ContentIsStillDelivered(t *testing.T) {
	fsys, dir, root := stageSignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	mutateAnItemFile(t, fsys, dir)

	read := readTree(t, fsys, "/bundles", root)
	require.NotNil(t, read.Bundle)
	frag, ok := read.Bundle.Fragments["house-style"]
	require.True(t, ok, "the author's own fragment must still be delivered")
	assert.Contains(t, frag.Content, "FRAG-BODY-MARKER")
}

// TestLocalTree_WithoutAManifest_KeepsItsEnvelopeFacts. A tree that carries no
// manifest has nothing to check the items against, and inventing an invalid
// verdict for it would flag every directory bundle authored before manifests
// existed.
func TestLocalTree_WithoutAManifest_KeepsItsEnvelopeFacts(t *testing.T) {
	fsys, _, _ := func() (afero.Fs, string, signing.TrustRoot) {
		f, d := stageUnsignedTree(t, "/bundles", "FRAG-BODY-MARKER")
		return f, d, nil
	}()

	read := readTree(t, fsys, "/bundles", nil)
	assert.Equal(t, SignatureNone, read.Signature(),
		"no manifest and no sibling signature is UNSIGNED, not invalid")
	assert.Equal(t, SignerNone, read.Signer())
	require.Equal(t, "FRAG-BODY-MARKER", read.Bundle.Fragments["house-style"].Content)
}

// TestLocalTree_AddedFileIsCaughtToo: the manifest is checked in BOTH
// directions, so a file smuggled into a signed tree is as much a mismatch as an
// edited one. Verifying only the claimed files would let an attacker ADD
// content to a bundle a human already approved.
func TestLocalTree_AddedFileIsCaughtToo(t *testing.T) {
	fsys, dir, root := stageSignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	testsupport.WriteFileString(t, fsys, filepath.Join(dir, "fragments", "smuggled.md"), "SMUGGLED\n", 0o644)

	read := readTree(t, fsys, "/bundles", root)
	assert.Equal(t, SignatureInvalid, read.Signature(),
		"a file added to a signed tree must invalidate it")
}

// --- one signature per bundle --------------------------------------------

// TestLocalTree_WithOnlyASiblingSignature_IsRefusedUntilReSigned: the sibling
// bundle.yaml.sig is retired — no reader parses two signature shapes. A tree
// still carrying one is not silently read as unsigned (the author believes it
// signed): it is REFUSED, and the refusal names the remedy, re-signing, which
// writes the manifest entry and removes the sibling.
func TestLocalTree_WithOnlyASiblingSignature_IsRefusedUntilReSigned(t *testing.T) {
	fsys, dir := stageUnsignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	signer, _ := testSkillSigner(t)
	envelope, err := afero.ReadFile(fsys, filepath.Join(dir, DirectoryFormManifest))
	require.NoError(t, err)
	armored, err := signing.Sign(envelope, signer, signing.NamespacePublish)
	require.NoError(t, err)
	testsupport.WriteFileString(t, fsys, filepath.Join(dir, DirectoryFormManifest)+".sig", string(armored), 0o644)

	mark := strictness.Checkpoint()
	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())

	require.NoError(t, err, "one refused bundle does not fail the read of the set")
	require.Empty(t, reads, "the tree is refused, not read as unsigned")
	found := strictness.Since(mark)
	require.NotEmpty(t, found, "the refusal is a bundle finding, never a silent skip")
	assert.Contains(t, found[0].Message, ErrSiblingSignatureRetired.Error(), "the refusal is reported by its sentinel")
	assert.Contains(t, found[0].Message, "re-sign", "and it names the remedy")
}
