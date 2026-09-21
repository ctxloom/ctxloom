package bundles

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The bundle document every reader test reads, and its exact bytes — a
// signature covers BYTES, so the fixture has to hand out the same ones it wrote.
var readerBundleYAML = []byte("version: \"1.0\"\nfragments:\n  keeper:\n    content: KEEPER-PAYLOAD\n")

// readerLoadoutDoc is readerBundleYAML as a companion's loadout DOCUMENT —
// the same bundle under run:, the shape ParseLoadout reads.
var readerLoadoutDoc = []byte("run:\n  version: \"1.0\"\n  fragments:\n    keeper:\n      content: KEEPER-PAYLOAD\n")

// readerTreeEnvelope and readerTreeFragments are readerBundleYAML's TREE
// counterpart: the same bundle — one fragment "keeper" carrying
// KEEPER-PAYLOAD — expressed the only way a bundle can now be published, with
// the envelope carrying no inline items and the fragment in a file beside it.
//
// They are separate values rather than a converted readerBundleYAML because
// the document form is no longer readable at all: the repofs fixtures need the
// tree, and the remaining readerBundleYAML users are the DOCUMENT-form readers
// (project, companion), which still read documents legitimately.
const readerTreeEnvelope = "version: \"1.0\"\n"

var readerTreeFragments = map[string]string{"keeper": "KEEPER-PAYLOAD"}

// signFor signs data with a throwaway key, returning the armored signature and
// a trust root that authorizes that key to publish as principal. Real crypto,
// so "valid" and "trusted" are facts here rather than fixture conventions.
func signFor(t *testing.T, data []byte, principal string) ([]byte, trust.TrustRoot, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	armored, err := signing.Sign(data, sshSigner, signing.NamespacePublish)
	require.NoError(t, err)
	return armored, allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	}), sshPub
}

// treeSignerFor is signFor's TREE counterpart: a throwaway key, plus the trust
// root that authorizes it to publish as principal. It returns the SIGNER rather
// than a detached signature because a tree is signed over its own manifest, by
// attest.SignBundle, at staging time — there is no separate payload to sign
// ahead of the tree existing.
func treeSignerFor(t *testing.T, principal string) (ssh.Signer, trust.TrustRoot, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return sshSigner, allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	}), sshPub
}

// ---------------------------------------------------------------------------
// The axes cannot be minted, and an unpopulated read claims nothing.
// ---------------------------------------------------------------------------

// TestBundleRead_TrustAxesCannotBeMintedByACaller is the structural half of the
// rule: the three axes are unexported, so no caller anywhere — in this module or
// out of it — can write "local" into a struct literal and have the loader
// believe it. This is the same property, for the same reason, that
// TestParseBundle_YAMLCannotForgeUntrustedSignerFingerprint pins for a bundle
// file's claim about its own signer.
//
// It is asserted by REFLECTION rather than by "it does not compile", because a
// compile-time proof cannot be written as a test that fails when the field is
// exported — the file simply stops building, which reads as a broken test
// rather than as a violated invariant.
func TestBundleRead_TrustAxesCannotBeMintedByACaller(t *testing.T) {
	rt := reflect.TypeOf(BundleRead{})
	for _, name := range []string{"trustCtx", "signature", "signer", "ref"} {
		field, ok := rt.FieldByName(name)
		require.True(t, ok, "BundleRead must still carry %s", name)
		assert.NotEmpty(t, field.PkgPath,
			"BundleRead.%s must stay UNEXPORTED: an exported one lets any caller mint trust facts "+
				"out of a struct literal, which is a trust bypass that reviews as data", name)
	}
}

// The zero value is the honest one: an unpopulated read claims nothing on any
// axis, and must not read as "local, unsigned, no signer" — which is a claim.
func TestBundleRead_ZeroValueClaimsNothing(t *testing.T) {
	var zero BundleRead

	assert.Equal(t, TrustCtxUnset, zero.TrustCtx())
	assert.Equal(t, SignatureUnset, zero.Signature())
	assert.Equal(t, SignerUnset, zero.Signer())
	assert.False(t, zero.Claimed(), "a read nobody established anything about must not pass as established")
}

// And the loader ACTS on that: an unclaimed read is withheld rather than
// admitted as unsigned local content, and the withhold is recorded as a
// fatal-class finding rather than being silent.
func TestLoader_WithholdsAnUnclaimedRead(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	mark := strictness.Checkpoint()

	forged := BundleRead{Bundle: &Bundle{Name: "forged", Version: "1.0"}, Provenance: ProvenanceProject}
	l := LoaderOf(Resolve(context.Background(), ledger(), staticReader{reads: []BundleRead{forged}}))

	assert.Empty(t, l.Reads(), "a read with no established trust facts must not become addressable content")
	_, err := l.Load("forged")
	assert.Error(t, err)
	assert.NotEmpty(t, strictness.Since(mark), "withholding it must be recorded, not silent")
}

// ---------------------------------------------------------------------------
// Each constructor hard-codes its own provenance and trust context.
// ---------------------------------------------------------------------------

// readerV1 is where a single-file document must be written for the reader to
// find it: the v1 FORMAT ROOT of the /bundles root these tests hand the reader,
// never the root itself.
func readerV1(leaf string) string {
	return filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), leaf)
}

func TestNewProjectReader_ReportsProjectProvenanceAndLocalContext(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, readerV1("kit.yaml"), readerBundleYAML, 0o644))

	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceProject, reads[0].Provenance)
	assert.Equal(t, TrustCtxLocal, reads[0].TrustCtx())
	assert.Equal(t, SignatureNone, reads[0].Signature(), "an unsigned project bundle reports none, never unset")
	assert.Equal(t, SignerNone, reads[0].Signer())
	assert.Equal(t, "KEEPER-PAYLOAD", reads[0].Bundle.Fragments["keeper"].Content)

	wantTyped, err := trust.LocalRef("kit")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, reads[0].SourceRef(),
		"a project bundle's typed source ref is LocalRef(its bare resolution name), minted by newRead's fallback")
}

func TestNewBuiltinReader_ReportsBuiltinProvenanceLocalAndUnsigned(t *testing.T) {
	reads, err := NewBuiltinReader().Read(context.Background())

	require.NoError(t, err)
	require.NotEmpty(t, reads, "the binary ships builtin bundles; an empty read means the embed is broken")
	for _, read := range reads {
		assert.Equal(t, ProvenanceBuiltin, read.Provenance)
		assert.Equal(t, TrustCtxLocal, read.TrustCtx(), "a builtin was compiled in; it crossed no intermediary")
		assert.Equal(t, SignatureNone, read.Signature(),
			"a builtin is deliberately unsigned — signing bytes with a key inside the binary that verifies them is circular")
		assert.Equal(t, SignerNone, read.Signer())
	}
}

func TestNewCompanionReader_ReportsCompanionProvenanceAndLocalContext(t *testing.T) {
	reads, err := NewCompanionReader(
		loadoutProbe(CompanionLoadout{Bin: "ltk", Document: readerLoadoutDoc}),
	).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceCompanion, reads[0].Provenance)
	assert.Equal(t, TrustCtxLocal, reads[0].TrustCtx(),
		"a loadout came off the stdout of a binary the user consented to execute — no intermediary")
	assert.Equal(t, "ctxloom:companion@ltk", reads[0].DisplayName())
	assert.Equal(t, SignatureNone, reads[0].Signature())
	assert.Equal(t, SignerNone, reads[0].Signer())

	wantTyped, err := trust.CompanionRef("ltk")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, reads[0].SourceRef(),
		"a companion loadout's typed source ref is CompanionRef(its binary name)")
}

func TestNewRepoFSReader_ReportsRemoteProvenanceAndRemoteContext(t *testing.T) {
	tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments, nil)

	reads, err := NewRepoFSReader(tree, "https://example.test/repo@bundles/kit", WithRepoURL(repoTreeURL)).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceRemote, reads[0].Provenance)
	assert.Equal(t, TrustCtxRemote, reads[0].TrustCtx(), "these bytes crossed a forge; that is the whole distinction")
	assert.Equal(t, "https://example.test/repo@bundles/kit", reads[0].DisplayName(), "canonical is the sole resolution identity")

	wantTyped, err := trust.GitRef("example.test", "/repo", "kit")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, reads[0].SourceRef(),
		"a repofs reader's typed source ref is GitRef(host, repo path, bundle) from its own canonical ref")
}

// ---------------------------------------------------------------------------
// Every reader reports TRUTHS on all three axes.
// ---------------------------------------------------------------------------

// The repofs reader is where the signature facts gate, so all four reachable
// (signature, signer) pairs are pinned against real keys and real signatures.
func TestNewRepoFSReader_SignatureFactsAreEstablishedNotAssumed(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	signer, root, pub := treeSignerFor(t, "publisher@example.test")

	t.Run("no signature is none/none", func(t *testing.T) {
		tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments, nil)
		reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL), WithTrustRoot(root)).Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureNone, reads[0].Signature())
		assert.Equal(t, SignerNone, reads[0].Signer())
		assert.Empty(t, reads[0].Bundle.Signer())
	})

	t.Run("trusted key over these bytes is valid/trusted", func(t *testing.T) {
		tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments, signer)
		reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL), WithTrustRoot(root)).Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureValid, reads[0].Signature())
		assert.Equal(t, SignerTrusted, reads[0].Signer())
		assert.Equal(t, "publisher@example.test", reads[0].Bundle.Signer(),
			"the verified principal comes from the trust root, never from the artifact")
	})

	t.Run("a key nothing trusts is valid/untrusted and names the key", func(t *testing.T) {
		tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments, signer)
		reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL)).Read(context.Background()) // no trust root
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureValid, reads[0].Signature(), "the signature covers the bytes; who made it is a separate fact")
		assert.Equal(t, SignerUntrusted, reads[0].Signer())
		assert.Equal(t, ssh.FingerprintSHA256(pub), reads[0].UntrustedSignerFingerprint(),
			"the display-only fingerprint names the key a human would be trusting")
		assert.Empty(t, reads[0].Bundle.Signer(), "and naming it must not have granted it anything")
	})

	// THE FOURTH PAIR IS GONE FROM THIS READER, and its absence is the point.
	//
	// The document form reported "a trusted key over other bytes" as CONTENT
	// carrying SignatureInvalid, leaving it to a later stage to withhold. A
	// tree cannot say that: attest.VerifyBundle checks the tree against its
	// signed manifest in both directions, so bytes that moved are caught at the
	// READ and the read fails. SignatureInvalid is therefore unreachable for a
	// repofs read — the state is not withheld, it no longer exists here.
	//
	// That is strictly stronger and it is what this now pins: the same tamper,
	// refused outright rather than reported and cleaned up afterwards.
	t.Run("a trusted key over other bytes is refused, not reported", func(t *testing.T) {
		tree := repoTreeTamperedAfterSigning(t, "kit", readerTreeEnvelope, readerTreeFragments, signer)

		_, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL), WithTrustRoot(root)).Read(context.Background())
		require.Error(t, err, "an edited item file must never read as content")
		assert.ErrorIs(t, err, ErrTreeBundleWithheld,
			"the KEY is still trusted — it is the bytes that moved, and the withheld sentinel is what keeps that from degrading to unsigned")
	})
}

// ---------------------------------------------------------------------------
// Companion posture: reported, never withheld.
// ---------------------------------------------------------------------------

// A companion loadout whose signature does not verify is DELIVERED, with the
// failure said out loud. Withholding it would punish the user for the
// companion's build error; the control that catches a swapped binary is the
// hash-keyed exec consent, not this.
func TestNewCompanionReader_InvalidSignatureIsReportedNotWithheld(t *testing.T) {
	signed := []byte("run:\n  version: \"1.0\"\n  fragments:\n    ltk:\n      content: OLD\n")
	shipped := []byte("run:\n  version: \"1.0\"\n  fragments:\n    ltk:\n      content: NEW\n")
	sig, root, _ := signFor(t, signed, "ltk@example.test")

	var warnings bytes.Buffer
	reads, err := NewCompanionReader(
		loadoutProbe(CompanionLoadout{Bin: "ltk", Document: shipped, Signature: sig}),
		WithTrustRoot(root),
		captureWarnings(&warnings),
	).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1, "a companion's content must still be delivered when its signature does not verify")
	assert.Equal(t, "NEW", reads[0].Bundle.Fragments["ltk"].Content, "the SHIPPED bytes are delivered, not the signed ones")
	assert.Equal(t, SignatureInvalid, reads[0].Signature())
	assert.Empty(t, reads[0].Bundle.Signer(), "an unverifiable signature attributes nobody")
	assert.Contains(t, warnings.String(), "does not verify over its own bytes")
	assert.Contains(t, warnings.String(), "stale or mismatched signature",
		"the warning must name the likely cause a companion author can act on")
	assert.NotContains(t, warnings.String(), "tamper",
		"this is a build-error signal, not an attack signal, and must not be phrased as tampering")
}

// A loadout whose BYTES will not parse produced no content at all: there is
// nothing to report, so it is warned about and skipped — never fatal, never a
// crash, and never silent.
func TestNewCompanionReader_UnparseableLoadoutIsWarnedAndSkipped(t *testing.T) {
	var warnings bytes.Buffer
	reads, err := NewCompanionReader(
		loadoutProbe(
			CompanionLoadout{Bin: "broken", Document: []byte(":\n  not a loadout")},
			CompanionLoadout{Bin: "ltk", Document: readerLoadoutDoc},
		),
		captureWarnings(&warnings),
	).Read(context.Background())

	require.NoError(t, err, "one broken companion must never sink the read")
	require.Len(t, reads, 1, "the healthy companion still contributes")
	assert.Equal(t, "ctxloom:companion@ltk", reads[0].DisplayName())
	assert.Contains(t, warnings.String(), "broken", "the companion that produced nothing must be named")
}

// ---------------------------------------------------------------------------
// The two rows the loader itself acts on.
// ---------------------------------------------------------------------------

// remote | invalid | trusted -> WITHHOLD. A signature that fails over remote
// bytes is tamper and must never degrade to the unsigned/review path: degrading
// it lets an attacker downgrade signed content to merely-reviewable content by
// corrupting a `.sig`.
//
// The READ still reports it — no reader drops, and a bundle nobody can see is a
// bundle nobody can diagnose — and the PROCESS stage withholds every item in it.
// That is the half asserted here: the read carries remote|invalid honestly, the
// delivery path answers ErrFragmentWithheld, and the withhold raises a trust
// finding rather than vanishing. The production decision that returns
// ReasonTampered lives in internal/adapters/operations and is pinned there.
func TestLoader_RemoteTamperedTreeIsRefusedNotDegradedToUnsigned(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	const ref = "https://example.test/repo@bundles/kit"
	signer, root, _ := treeSignerFor(t, "publisher@example.test")
	tree := repoTreeTamperedAfterSigning(t, "kit", readerTreeEnvelope, readerTreeFragments, signer)

	mark := strictness.Checkpoint()
	l := LoaderOf(Resolve(context.Background(), ledger(), NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL), WithTrustRoot(root), WithReaderReporter(ledger()))))

	// The REFUSAL moved earlier than it used to sit, and this is what changed:
	// the document form reported a tamper as content carrying SignatureInvalid
	// and left a later stage to withhold it. A tree is checked against its
	// signed manifest at the READ, so there is no read at all — the attack has
	// nowhere downstream to be mishandled.
	assert.Empty(t, l.Reads(), "a tree whose files moved must never become content")

	pipe := NewPipeline(l, signatureRowsAuthorizer(), LinksUnchecked(), false)
	_, ferr := pipe.GetFragment(ref + "#fragments/keeper")
	require.Error(t, ferr, "and it must not resolve as an unsigned bundle awaiting review")

	// The loud half. A refusal that nothing reported would be a silent drop
	// wearing a security check's name, which is the failure this whole pairing
	// exists to prevent.
	findings := strictness.Since(mark)
	require.NotEmpty(t, findings, "the refusal is a finding, not a silent drop")
	var reported string
	for _, f := range findings {
		reported += f.Message + "\n"
	}
	assert.Contains(t, reported, "does not match what was signed",
		"the finding must say the content disagrees with the signature, not merely that a read failed")
	assert.Contains(t, reported, "fragments/keeper.md",
		"and it must name the file that moved — a tamper report an operator cannot localise is not actionable "+
			"(this also proves the fixture actually tampered, so the assertion above cannot pass vacuously)")
}

// local | invalid | trusted -> ADMIT + WARN. The author edited and did not
// re-sign: their content is theirs and still arrives, and they are told at the
// moment it stopped being publishable rather than at publish time.
func TestLoader_LocalInvalidSignatureIsAdmittedAndTheAuthorIsTold(t *testing.T) {
	fsys, dir, root := stageSignedTree(t, "/bundles", "KEEPER-PAYLOAD")
	mutateAnItemFile(t, fsys, dir)

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)
	pipe := NewPipeline(NewLoader(NewProjectReader(fsys, []string{"/bundles"}, WithTrustRoot(root), WithReaderReporter(ledger()))).WithReporter(ledger()),
		signatureRowsAuthorizer(), LinksUnchecked(), false)

	lc, err := pipe.GetFragment(verifyTreeName + "#fragments/house-style")

	require.NoError(t, err, "locality already answered the trust question; a stale manifest cannot withhold")
	assert.Contains(t, lc.Content, "KEEPER-PAYLOAD")
	assert.Contains(t, warnings.String(), content.ManifestPath)
	assert.Contains(t, warnings.String(), "ctxloom bundle sign "+verifyTreeName, "the warning must name the command that fixes it")
}

// captureWarnings returns a reader option that funnels a reader's diagnostics
// into buf, so a test can read what the user was told.
func captureWarnings(buf *bytes.Buffer) ReaderOption {
	return WithReaderReporter(report.SinkFunc(func(f report.Finding) {
		fmt.Fprintln(buf, f.Text)
	}))
}
