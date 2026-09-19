package bundles

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// A bundle in the project's own content tree is trusted BY VIRTUE OF BEING
// LOCAL — the decision function allows it at its local step, above every
// signature step, and no filesystem load path verifies a publisher signature at
// all. So a stale or invalid sibling `.sig` cannot and must not withhold it.
//
// What it MUST do is say something. A signature that no longer covers its bytes
// is a promotion failure waiting to happen: `bundle push` and `bundle move --to`
// both refuse to publish that pair (they would hand every consumer a tamper
// alarm), so the author finds out at the moment they publish rather than at the
// moment they broke it. Saying it at load turns a deferred, confusing failure
// into an immediate, actionable one — and, unlike a withhold, costs the user
// none of their content.
//
// Every test below therefore asserts BOTH halves: the content is delivered, AND
// the diagnostic is (or is not) emitted. Asserting only the warning would let a
// regression that withholds the content pass.
//
// The diagnostic RIDES THE VERDICT: the Authorizer answers the decision
// table's `local | invalid | *` row with {Allow: true, Reason:
// ReasonStaleLocalSignature, Detail: StaleSignatureAdvice}, and the delivery
// path prints Detail. So these tests drive a Pipeline, which is the only place
// both halves are observable at once. signatureRowsAuthorizer below is the row
// itself, spelled locally; the production decision that produces it is pinned
// in internal/adapters/operations.

// signatureRowsAuthorizer is the two decision-table rows that key on the signature
// axis, and nothing else:
//
//	local  | invalid | * -> ADMIT + WARN  (locality already answered the trust
//	                                       question; the author has to be told)
//	remote | invalid | * -> WITHHOLD      (tamper — never degraded to unsigned)
//
// Everything else admits plainly. It is the ROWS, spelled here so a test can
// observe the delivery path acting on a Verdict; the production decision that
// produces these verdicts is pinned in internal/adapters/operations.
func signatureRowsAuthorizer() Authorizer {
	return authorizerFunc(func(e Exposure) Verdict {
		if e.Read.Signature() != SignatureInvalid {
			return admitVerdict()
		}
		if e.Read.TrustCtx() == TrustCtxRemote {
			return Verdict{Reason: ReasonTampered, Detail: e.Read.SignatureDetail()}
		}
		return Verdict{Allow: true, Reason: ReasonStaleLocalSignature, Detail: StaleSignatureAdvice(e.Read)}
	})
}

// deliverKeeper resolves the fixture bundle's one fragment through a pipeline
// carrying signatureRowsAuthorizer, capturing everything the user was told, and returns
// the delivered content plus those diagnostics.
func deliverKeeper(t *testing.T, fsys afero.Fs, bundleName string) (*LoadedContent, string) {
	t.Helper()
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	pipe := NewPipeline(NewLoader(NewProjectReader(fsys, []string{"/bundles"}, WithReaderReporter(ledger()))).WithReporter(ledger()),
		signatureRowsAuthorizer(), LinksUnchecked(), false)
	lc, err := pipe.GetFragment(bundleName + "#fragments/keeper")
	require.NoError(t, err, "a signature fact about LOCAL content must never withhold it")
	return lc, warnings.String()
}

// signBytesFor signs data under the publish namespace with a throwaway key and
// returns the armored detached signature — real crypto, so "covers these bytes"
// is a fact rather than a fixture convention.
// localTreeFixture stages a tree-form bundle named name, signed through its
// ONE signature (the SHA256SUMS manifest and its .sigs/ entry) when signed is
// true, and returns the filesystem and the bundle directory.
func localTreeFixture(t *testing.T, name string, signed bool) (afero.Fs, string) {
	t.Helper()
	mem := afero.NewMemMapFs()
	v2 := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, mem.MkdirAll(v2, 0o755))
	st, err := content.NewTreeStore(mem, v2, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		trust.Ref{Bundle: name, Kind: trust.KindFragment, Name: "keeper"},
		signing.FormRaw,
		content.Fragment{Name: "keeper", ItemMeta: content.ItemMeta{Body: "KEEPER-PAYLOAD"}}))
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), DirectoryFormManifest,
		[]byte("name: "+name+"\nversion: 2.0.0\n")))
	if signed {
		signer, _ := testSkillSigner(t)
		b, err := st.Open(context.Background(), content.BundleID(name))
		require.NoError(t, err)
		require.NoError(t, attest.SignBundle(context.Background(), st, b, signer))
	}
	return mem, filepath.Join(v2, name)
}

// editItemFile changes the keeper fragment's file after signing — the
// author's edit that stales the manifest.
func editItemFile(t *testing.T, mem afero.Fs, dir string) {
	t.Helper()
	require.NotEmpty(t, mutateAnItemFile(t, mem, dir))
}

func TestLoader_LoadFile_StaleLocalSignature_WarnsAndDelivers(t *testing.T) {
	mem, dir := localTreeFixture(t, "stale-tools", true)
	editItemFile(t, mem, dir)

	lc, warnings := deliverKeeper(t, mem, "stale-tools")

	require.NotNil(t, lc)
	assert.Equal(t, "KEEPER-PAYLOAD\nTAMPERED\n", lc.Content,
		"the content must be delivered — locality already answered the trust question")
	assert.Contains(t, warnings, "ctxloom: warning:")
	assert.Contains(t, warnings, content.ManifestPath, "the warning names the manifest the files no longer match")
	assert.Contains(t, warnings, "ctxloom bundle sign stale-tools",
		"the warning must name the command that fixes it")
}

func TestLoader_LoadFile_ValidLocalSignature_Silent(t *testing.T) {
	mem, _ := localTreeFixture(t, "valid-tools", true)

	lc, warnings := deliverKeeper(t, mem, "valid-tools")

	assert.Equal(t, "KEEPER-PAYLOAD", lc.Content)
	assert.Empty(t, warnings, "a signature that still covers the files is not a problem")
}

func TestLoader_LoadFile_UnsignedLocalBundle_Silent(t *testing.T) {
	mem, _ := localTreeFixture(t, "plain-tools", false)

	lc, warnings := deliverKeeper(t, mem, "plain-tools")

	assert.Equal(t, "KEEPER-PAYLOAD", lc.Content)
	assert.Empty(t, warnings)
}

func TestLoader_LoadFile_CorruptLocalSignature_WarnsAndDelivers(t *testing.T) {
	mem, dir := localTreeFixture(t, "corrupt-tools", true)
	entries, err := afero.ReadDir(mem, filepath.Join(dir, content.SigDirName))
	require.NoError(t, err)
	require.NotEmpty(t, entries, "the fixture signed the manifest")
	testsupport.WriteFile(t, mem, filepath.Join(dir, content.SigDirName, entries[0].Name()), []byte("not a signature\n"), 0o644)

	lc, warnings := deliverKeeper(t, mem, "corrupt-tools")

	assert.Equal(t, "KEEPER-PAYLOAD", lc.Content)
	assert.Contains(t, warnings, content.ManifestPath)
}

func TestLoader_LoadFile_StaleLocalSignature_WarnsOncePerBundle(t *testing.T) {
	mem, dir := localTreeFixture(t, "repeat-tools", true)
	editItemFile(t, mem, dir)

	_, first := deliverKeeper(t, mem, "repeat-tools")
	_, second := deliverKeeper(t, mem, "repeat-tools")

	assert.Contains(t, first, content.ManifestPath)
	assert.Empty(t, second, "the same stale tree must not be reported twice in one process")
}

// TestLoader_LoadFile_SiblingSignature_IsRefused: the retired sibling
// signature is not read past. A single-file bundle carrying one is refused —
// re-signing is the upgrade path, and a single-file bundle takes the tree
// form to be signed.
func TestLoader_LoadFile_SiblingSignature_IsRefused(t *testing.T) {
	mem := afero.NewMemMapFs()
	v2 := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, mem.MkdirAll(v2, 0o755))
	path := filepath.Join(v2, "old-tools.yaml")
	testsupport.WriteFile(t, mem, path, []byte("version: \"1.0\"\nfragments:\n  keeper:\n    content: KEEPER-PAYLOAD\n"), 0o644)
	testsupport.WriteFile(t, mem, path+".sig", []byte("armored-signature-bytes"), 0o644)

	mark := strictness.Checkpoint()
	reads, err := NewProjectReader(mem, []string{"/bundles"}, WithReaderReporter(ledger())).Read(context.Background())

	require.NoError(t, err)
	assert.Empty(t, reads, "a bundle with a retired sibling signature is refused, not read as unsigned")
	found := strictness.Since(mark)
	require.NotEmpty(t, found)
	assert.Contains(t, found[0].Message, ErrSiblingSignatureRetired.Error())
}
