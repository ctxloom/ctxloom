package bundles

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The bundle document every reader test reads, and its exact bytes — readers
// hand back the bytes they read, so the fixture has to hand out the same ones it
// wrote.
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
const readerTreeEnvelope = "version: 1.0.0\n"

var readerTreeFragments = map[string]string{"keeper": "KEEPER-PAYLOAD"}

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
	for _, name := range []string{"locality", "ref"} {
		field, ok := rt.FieldByName(name)
		require.True(t, ok, "BundleRead must still carry %s", name)
		assert.NotEmpty(t, field.PkgPath,
			"BundleRead.%s must stay UNEXPORTED: an exported one lets any caller mint trust facts "+
				"out of a struct literal, which is a trust bypass that reviews as data", name)
	}
}

// The zero value is the honest one: an unpopulated read claims nothing on any
// axis, and must not read as "local" — which is a claim.
func TestBundleRead_ZeroValueClaimsNothing(t *testing.T) {
	var zero BundleRead

	assert.Equal(t, LocalityUnset, zero.Locality())
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

// readerRoot is where a tree must be written for the reader to
// find it: the FORMAT ROOT of the /bundles root these tests hand the reader,
// never the root itself.
func readerRoot() string {
	return paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
}

func TestNewProjectReader_ReportsProjectProvenanceAndLocalContext(t *testing.T) {
	fsys := afero.NewMemMapFs()
	writeTree(t, fsys, readerRoot(), "kit", string(readerBundleYAML))

	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceProject, reads[0].Provenance)
	assert.Equal(t, LocalityLocal, reads[0].Locality())
	assert.Equal(t, "KEEPER-PAYLOAD", reads[0].Bundle.Fragments["keeper"].Content)

	wantTyped, err := ident.LocalRef("kit")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, reads[0].SourceRef(),
		"a project bundle's typed source ref is LocalRef(its bare resolution name), minted by newRead's fallback")
}

func TestNewCompanionReader_ReportsCompanionProvenanceAndLocalContext(t *testing.T) {
	reads, err := NewCompanionReader(
		loadoutProbe(CompanionLoadout{Bin: "ltk", Document: readerLoadoutDoc}),
	).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceCompanion, reads[0].Provenance)
	assert.Equal(t, LocalityLocal, reads[0].Locality(),
		"a loadout came off the stdout of a binary the user consented to execute — no intermediary")
	assert.Equal(t, "ctxloom:companion@ltk", reads[0].DisplayName())

	wantTyped, err := ident.CompanionRef("ltk")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, reads[0].SourceRef(),
		"a companion loadout's typed source ref is CompanionRef(its binary name)")
}

func TestNewRepoFSReader_ReportsRemoteProvenanceAndRemoteContext(t *testing.T) {
	tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments)

	reads, err := NewRepoFSReader(tree, "https://example.test/repo@bundles/kit", WithRepoURL(repoTreeURL)).Read(context.Background())

	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, ProvenanceRemote, reads[0].Provenance)
	assert.Equal(t, LocalityRemote, reads[0].Locality(), "these bytes crossed a forge; that is the whole distinction")
	assert.Equal(t, "https://example.test/repo@bundles/kit", reads[0].DisplayName(), "canonical is the sole resolution identity")

	wantTyped, err := ident.GitRef("example.test", "/repo", "kit")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, reads[0].SourceRef(),
		"a repofs reader's typed source ref is GitRef(host, repo path, bundle) from its own canonical ref")
}

// ---------------------------------------------------------------------------
// Every reader reports TRUTHS on all three axes.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Companion posture: reported, never withheld.
// ---------------------------------------------------------------------------

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

// captureWarnings returns a reader option that funnels a reader's diagnostics
// into buf, so a test can read what the user was told.
func captureWarnings(buf *bytes.Buffer) ReaderOption {
	return WithReaderReporter(report.SinkFunc(func(f report.Finding) {
		fmt.Fprintln(buf, f.Text)
	}))
}
