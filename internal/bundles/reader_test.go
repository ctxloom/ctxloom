package bundles

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// The bundle document every reader test reads, and its exact bytes — a
// signature covers BYTES, so the fixture has to hand out the same ones it wrote.
var readerBundleYAML = []byte("version: \"1.0\"\nfragments:\n  keeper:\n    content: KEEPER-PAYLOAD\n")

// signFor signs data with a throwaway key, returning the armored signature and
// a trust root that authorizes that key to publish as principal. Real crypto,
// so "valid" and "trusted" are facts here rather than fixture conventions.
func signFor(t *testing.T, data []byte, principal string) ([]byte, signing.TrustRoot, ssh.PublicKey) {
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
	l := NewLoader(staticReader{reads: []BundleRead{forged}})

	assert.Empty(t, l.Reads(), "a read with no established trust facts must not become addressable content")
	_, err := l.Load("forged")
	assert.Error(t, err)
	assert.NotEmpty(t, strictness.Since(mark), "withholding it must be recorded, not silent")
}

// ---------------------------------------------------------------------------
// Each constructor hard-codes its own provenance and trust context.
// ---------------------------------------------------------------------------

func TestNewProjectReader_ReportsProjectProvenanceAndLocalContext(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/kit.yaml", readerBundleYAML, 0o644))

	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceProject, reads[0].Provenance)
	assert.Equal(t, TrustCtxLocal, reads[0].TrustCtx())
	assert.Equal(t, SignatureNone, reads[0].Signature(), "an unsigned project bundle reports none, never unset")
	assert.Equal(t, SignerNone, reads[0].Signer())
	assert.Equal(t, "KEEPER-PAYLOAD", reads[0].Bundle.Fragments["keeper"].Content)
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
	reads, err := NewCompanionReader(func(context.Context) ([]CompanionLoadout, error) {
		return []CompanionLoadout{{Bin: "ltk", Bundle: readerBundleYAML}}, nil
	}).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceCompanion, reads[0].Provenance)
	assert.Equal(t, TrustCtxLocal, reads[0].TrustCtx(),
		"a loadout came off the stdout of a binary the user consented to execute — no intermediary")
	assert.Equal(t, "ctxloom:companion@ltk", reads[0].Ref())
	assert.Equal(t, SignatureNone, reads[0].Signature())
	assert.Equal(t, SignerNone, reads[0].Signer())
}

func TestNewRepoFSReader_ReportsRemoteProvenanceAndRemoteContext(t *testing.T) {
	tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": readerBundleYAML})
	require.NoError(t, err)

	reads, err := NewRepoFSReader(tree, "https://example.test/repo@bundles/kit").Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceRemote, reads[0].Provenance)
	assert.Equal(t, TrustCtxRemote, reads[0].TrustCtx(), "these bytes crossed a forge; that is the whole distinction")
	assert.Equal(t, "https://example.test/repo@bundles/kit", reads[0].Ref(), "canonical is the sole resolution identity")
}

// ---------------------------------------------------------------------------
// Every reader reports TRUTHS on all three axes.
// ---------------------------------------------------------------------------

// The repofs reader is where the signature facts gate, so all four reachable
// (signature, signer) pairs are pinned against real keys and real signatures.
func TestNewRepoFSReader_SignatureFactsAreEstablishedNotAssumed(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	sig, root, pub := signFor(t, readerBundleYAML, "publisher@example.test")

	t.Run("no signature is none/none", func(t *testing.T) {
		tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": readerBundleYAML})
		require.NoError(t, err)
		reads, err := NewRepoFSReader(tree, ref, WithTrustRoot(root)).Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureNone, reads[0].Signature())
		assert.Equal(t, SignerNone, reads[0].Signer())
		assert.Empty(t, reads[0].Bundle.Signer())
	})

	t.Run("trusted key over these bytes is valid/trusted", func(t *testing.T) {
		tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": readerBundleYAML, "kit.yaml.sig": sig})
		require.NoError(t, err)
		reads, err := NewRepoFSReader(tree, ref, WithTrustRoot(root)).Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureValid, reads[0].Signature())
		assert.Equal(t, SignerTrusted, reads[0].Signer())
		assert.Equal(t, "publisher@example.test", reads[0].Bundle.Signer(),
			"the verified principal comes from the trust root, never from the artifact")
	})

	t.Run("a key nothing trusts is valid/untrusted and names the key", func(t *testing.T) {
		tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": readerBundleYAML, "kit.yaml.sig": sig})
		require.NoError(t, err)
		reads, err := NewRepoFSReader(tree, ref).Read(context.Background()) // no trust root
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureValid, reads[0].Signature(), "the signature covers the bytes; who made it is a separate fact")
		assert.Equal(t, SignerUntrusted, reads[0].Signer())
		assert.Equal(t, ssh.FingerprintSHA256(pub), reads[0].UntrustedSignerFingerprint(),
			"the display-only fingerprint names the key a human would be trusting")
		assert.Empty(t, reads[0].Bundle.Signer(), "and naming it must not have granted it anything")
	})

	t.Run("a trusted key over other bytes is invalid/trusted", func(t *testing.T) {
		edited := append(append([]byte{}, readerBundleYAML...), []byte("# edited after signing\n")...)
		tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": edited, "kit.yaml.sig": sig})
		require.NoError(t, err)
		reads, err := NewRepoFSReader(tree, ref, WithTrustRoot(root)).Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assert.Equal(t, SignatureInvalid, reads[0].Signature())
		assert.Equal(t, SignerTrusted, reads[0].Signer(),
			"the KEY is still trusted — it is the bytes that moved, and telling those apart is why there are two axes")
		assert.NotEmpty(t, reads[0].SignatureDetail(), "the reason has to travel with the fact")
	})
}

// A local reader reports its signature facts too. They do not gate — the
// content is delivered either way — but "we established nothing" and "we
// established that this is stale" are different, and only the second can be
// told to the author.
func TestNewProjectReader_ReportsSignatureFactsAsDiagnostics(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/kit.yaml", readerBundleYAML, 0o644))
	sig, root, _ := signFor(t, readerBundleYAML, "author@example.test")
	require.NoError(t, afero.WriteFile(fsys, "/bundles/kit.yaml.sig", sig, 0o644))

	reads, err := NewProjectReader(fsys, []string{"/bundles"}, WithTrustRoot(root)).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, TrustCtxLocal, reads[0].TrustCtx())
	assert.Equal(t, SignatureValid, reads[0].Signature(), "a local reader checks and REPORTS; it just does not gate")
	assert.Equal(t, SignerTrusted, reads[0].Signer())
}

// ---------------------------------------------------------------------------
// Companion posture: reported, never withheld.
// ---------------------------------------------------------------------------

// A companion loadout whose signature does not verify is DELIVERED, with the
// failure said out loud. Withholding it would punish the user for the
// companion's build error; the control that catches a swapped binary is the
// hash-keyed exec consent, not this.
func TestNewCompanionReader_InvalidSignatureIsReportedNotWithheld(t *testing.T) {
	signed := []byte("version: \"1.0\"\nfragments:\n  ltk:\n    content: OLD\n")
	shipped := []byte("version: \"1.0\"\nfragments:\n  ltk:\n    content: NEW\n")
	sig, root, _ := signFor(t, signed, "ltk@example.test")

	var warnings bytes.Buffer
	reads, err := NewCompanionReader(
		func(context.Context) ([]CompanionLoadout, error) {
			return []CompanionLoadout{{Bin: "ltk", Bundle: shipped, Signature: sig}}, nil
		},
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
		func(context.Context) ([]CompanionLoadout, error) {
			return []CompanionLoadout{
				{Bin: "broken", Bundle: []byte(":\n  not a bundle")},
				{Bin: "ltk", Bundle: readerBundleYAML},
			}, nil
		},
		captureWarnings(&warnings),
	).Read(context.Background())

	require.NoError(t, err, "one broken companion must never sink the read")
	require.Len(t, reads, 1, "the healthy companion still contributes")
	assert.Equal(t, "ctxloom:companion@ltk", reads[0].Ref())
	assert.Contains(t, warnings.String(), "broken", "the companion that produced nothing must be named")
}

// ---------------------------------------------------------------------------
// The two rows the loader itself acts on.
// ---------------------------------------------------------------------------

// remote | invalid | trusted -> WITHHOLD. A signature that fails over remote
// bytes is tamper and must never degrade to the unsigned/review path: degrading
// it lets an attacker downgrade signed content to merely-reviewable content by
// corrupting a `.sig`.
func TestLoader_RemoteInvalidSignatureIsWithheldNotDegradedToUnsigned(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	const ref = "https://example.test/repo@bundles/kit"
	sig, root, _ := signFor(t, readerBundleYAML, "publisher@example.test")
	edited := append(append([]byte{}, readerBundleYAML...), []byte("# substituted\n")...)
	tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": edited, "kit.yaml.sig": sig})
	require.NoError(t, err)

	mark := strictness.Checkpoint()
	l := NewLoader(NewRepoFSReader(tree, ref, WithTrustRoot(root)))

	assert.Empty(t, l.Reads(), "tampered remote content must not be addressable at all")
	_, lerr := l.Load(ref)
	assert.Error(t, lerr, "and it must not resolve as an unsigned bundle awaiting review")
	assert.NotEmpty(t, strictness.Since(mark), "the withhold is a trust finding, not a silent drop")
}

// local | invalid | trusted -> ADMIT + WARN. The author edited and did not
// re-sign: their content is theirs and still arrives, and they are told at the
// moment it stopped being publishable rather than at publish time.
func TestLoader_LocalInvalidSignatureIsAdmittedAndTheAuthorIsTold(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/wave6-stale.yaml", readerBundleYAML, 0o644))
	sig, root, _ := signFor(t, readerBundleYAML, "author@example.test")
	require.NoError(t, afero.WriteFile(fsys, "/bundles/wave6-stale.yaml.sig", sig, 0o644))
	edited := append(append([]byte{}, readerBundleYAML...), []byte("# edited, never re-signed\n")...)
	require.NoError(t, afero.WriteFile(fsys, "/bundles/wave6-stale.yaml", edited, 0o644))

	var warnings bytes.Buffer
	l := NewLoader(NewProjectReader(fsys, []string{"/bundles"}, WithTrustRoot(root))).WithWarnWriter(&warnings)

	b, err := l.Load("wave6-stale")

	require.NoError(t, err, "locality already answered the trust question; a stale sidecar cannot withhold")
	assert.Equal(t, "KEEPER-PAYLOAD", b.Fragments["keeper"].Content)
	assert.Contains(t, warnings.String(), "wave6-stale.yaml.sig")
	assert.Contains(t, warnings.String(), "ctxloom bundle sign wave6-stale", "the warning must name the command that fixes it")
}

// captureWarnings returns a reader option that funnels a reader's diagnostics
// into buf, so a test can read what the user was told.
func captureWarnings(buf *bytes.Buffer) ReaderOption {
	return WithReaderWarnings(func(format string, args ...any) {
		fmt.Fprintf(buf, format+"\n", args...)
	})
}
