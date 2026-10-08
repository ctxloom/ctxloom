package bundles

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectReaderOver writes one bundle document at dir/rel and returns a reader
// over dir, so a test can assert what a REAL read establishes rather than what a
// hand-built struct claims.
func projectReaderOver(t *testing.T, name, doc string) Reader {
	t.Helper()
	fsys := afero.NewMemMapFs()
	const dir = "/proj/content/bundles"
	writeTree(t, fsys, paths.BundlesLayoutRoot(dir, paths.LayoutV2), name, doc)
	return NewProjectReader(fsys, []string{dir})
}

func readOne(t *testing.T, r Reader) BundleRead {
	t.Helper()
	reads, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1, "the fixture must produce exactly one read")
	return reads[0]
}

// TestParseBundle_DeclaredNameIsAccepted pins that `name:` is a key a bundle may
// declare. The strict schema derives its vocabulary by reflection over Bundle's
// yaml tags, so this asserts the TAG through the behaviour it buys: a document
// declaring a name parses, and the value lands on the field.
//
// Before the retag this document was refused outright — "unknown key `name`" —
// and the bundle was skipped whole, which is how an acceptance fixture's hooks
// came to never load.
func TestParseBundle_DeclaredNameIsAccepted(t *testing.T) {
	b, err := ParseBundle([]byte("version: 1.0.0\nname: declared\n"))
	require.NoError(t, err, "a bundle must be allowed to declare its own name")
	assert.Equal(t, "declared", b.Name, "the declared name must land on Bundle.Name, not be dropped in silence")
}

// TestProjectReader_DeclaredNameWinsOverPath is the transitional rule: the file
// path is a FALLBACK identity, so it may not overwrite a name the document
// declared. The read's ref is unaffected — it stays the path-relative
// resolution identity — and the test asserts both, because collapsing the two
// is the mistake this arrangement exists to prevent.
func TestProjectReader_DeclaredNameWinsOverPath(t *testing.T) {
	read := readOne(t, projectReaderOver(t, "onpath", "version: 1.0.0\nname: declared\n"))

	assert.Equal(t, "declared", read.Bundle.Name, "a declared name must win over the path-derived one")
	assert.Equal(t, "onpath", read.DisplayName(), "the resolution ref stays path-derived; only Name is declared")
}

// TestProjectReader_UndeclaredNameFallsBackToPath is the other half of the same
// rule, and the one that keeps 69 existing bundle files working: a bundle that
// declares nothing still resolves under the name its path implies.
func TestProjectReader_UndeclaredNameFallsBackToPath(t *testing.T) {
	read := readOne(t, projectReaderOver(t, "onpath", "version: 1.0.0\n"))

	assert.Equal(t, "onpath", read.Bundle.Name, "a bundle declaring no name falls back to its path-derived name")
}

// TestNewRepoFSReader_DeclaredNameWinsOverTheCanonicalRef applies the same rule
// to pinned remote content. The canonical ref stays the SOLE resolution
// identity — it is what a profile authors and what the source identity is minted from — so
// the test asserts the ref is unmoved while the declared name lands.
func TestNewRepoFSReader_DeclaredNameWinsOverTheCanonicalRef(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	tree := repoTree(t, "kit", "version: \"1.0\"\nname: declared\n", map[string]string{"keeper": "KEEPER-PAYLOAD"})

	reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL)).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, "declared", reads[0].Bundle.Name, "a declared name must win over the canonical ref")
	assert.Equal(t, ref, reads[0].DisplayName(), "canonical stays the sole resolution identity")
}

// TestNewRepoFSReader_UndeclaredNameFallsBackToTheCanonicalRef is the other half:
// nothing that declares no name changes behaviour.
func TestNewRepoFSReader_UndeclaredNameFallsBackToTheCanonicalRef(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments)

	reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL)).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, ref, reads[0].Bundle.Name, "an undeclared remote bundle still falls back to its canonical ref")
}

// TestNewCompanionReader_DeclaredNameWinsOverTheCompanionRef applies the rule to
// a companion loadout, whose ctxloom:companion@<bin> ref is likewise the
// resolution identity and only the fallback for Name.
func TestNewCompanionReader_DeclaredNameWinsOverTheCompanionRef(t *testing.T) {
	probe := loadoutProbe(CompanionLoadout{Bin: "ltk", Document: []byte("run:\n  version: \"1.0\"\n  name: declared\n")})

	reads, err := NewCompanionReader(probe).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, "declared", reads[0].Bundle.Name, "a declared name must win over the companion ref")
	assert.Equal(t, companionRefPrefix+"ltk", reads[0].DisplayName(), "the companion ref stays the resolution identity")
}

// TestNewCompanionReader_UndeclaredNameFallsBackToTheCompanionRef is the other
// half for companions.
func TestNewCompanionReader_UndeclaredNameFallsBackToTheCompanionRef(t *testing.T) {
	probe := loadoutProbe(CompanionLoadout{Bin: "ltk", Document: readerLoadoutDoc})

	reads, err := NewCompanionReader(probe).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, companionRefPrefix+"ltk", reads[0].Bundle.Name,
		"an undeclared companion loadout still falls back to its companion ref")
}

// TestNewCompanionReader_WarnsOnlyForAnUnaddressableCompanion: a companion
// whose binary name cannot be minted into a reference is warned about, since
// its items will be withheld; one that can is not.
func TestNewCompanionReader_WarnsOnlyForAnUnaddressableCompanion(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	probe := loadoutProbe(
		CompanionLoadout{Bin: "ltk", Document: readerLoadoutDoc},
		CompanionLoadout{Bin: "", Document: readerLoadoutDoc},
	)

	_, err := NewCompanionReader(probe, WithReaderReporter(ledger())).Read(context.Background())
	require.NoError(t, err)

	assert.Contains(t, buf.String(), `cannot address source "`+companionRefPrefix+`"`)
	assert.NotContains(t, buf.String(), `cannot address source "`+companionRefPrefix+`ltk"`)
}

// TestProjectReader_SourceIdentityIsTheLocationNotTheDeclaredName pins
// newRead's source-ref stamp end to end, through the public reader and loader:
// a project bundle's source identity — BundleRead.SourceRef, Key, and every
// item ref minted from it — is the path-relative name it was FOUND under. The
// `name:` it declares is content and reaches none of them, so a project bundle
// that declares a remote bundle's canonical ref still addresses as local
// project content under its own location.
func TestProjectReader_SourceIdentityIsTheLocationNotTheDeclaredName(t *testing.T) {
	const remoteRef = "https://example.test/repo@bundles/kit"
	r := projectReaderOver(t, "impostor", "version: 1.0.0\nname: "+remoteRef+"\n"+
		"fragments:\n  keeper:\n    content: PROJECT-BODY\n")

	read := readOne(t, r)
	require.Equal(t, remoteRef, read.Bundle.Name, "fixture: the bundle must actually DECLARE the remote's ref")

	want, err := ident.LocalRef("impostor")
	require.NoError(t, err)
	assert.Equal(t, want, read.SourceRef(), "the source identity is the location, never the declared name")
	assert.Equal(t, want.BundleIdentity(), read.Key())

	items, err := NewLoader(r).ReadFragment("impostor#fragments/keeper")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "ctxloom+local:impostor#fragments/keeper", items[0].ItemRef,
		"the item ref is minted from the location-derived source identity")
	assert.NotContains(t, items[0].ItemRef, "example.test", "the declared remote ref must not leak into the address")
}

// TestNewRepoFSReader_SourceIdentityIsTheCanonicalRefNotTheDeclaredName is the
// pinned-remote counterpart: declaring a name moves neither the read's source
// identity nor its item refs off the canonical ref the tree was installed at.
func TestNewRepoFSReader_SourceIdentityIsTheCanonicalRefNotTheDeclaredName(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	read := func(envelope string) (BundleRead, string) {
		r := NewRepoFSReader(repoTree(t, "kit", envelope, map[string]string{"keeper": "K"}), ref, WithRepoURL(repoTreeURL))
		reads, err := r.Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		items, err := NewLoader(r).ReadFragment(ref + "#fragments/keeper")
		require.NoError(t, err)
		require.Len(t, items, 1)
		return reads[0], items[0].ItemRef
	}

	undeclared, undeclaredItem := read("version: \"1.0\"\n")
	declared, declaredItem := read("version: \"1.0\"\nname: impostor\n")
	require.Equal(t, "impostor", declared.Bundle.Name, "fixture: the bundle must actually declare a name")

	require.NotEqual(t, ident.BundleRef{}, undeclared.SourceRef(), "precondition: the canonical ref mints")
	assert.Equal(t, undeclared.SourceRef(), declared.SourceRef(), "a declared name must not move the source identity")
	assert.Equal(t, undeclaredItem, declaredItem)
	assert.NotContains(t, declaredItem, "impostor")
}
