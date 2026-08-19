package bundles

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/content"
)

// projectReaderOver writes one bundle document at dir/rel and returns a reader
// over dir, so a test can assert what a REAL read establishes rather than what a
// hand-built struct claims.
func projectReaderOver(t *testing.T, rel, doc string) Reader {
	t.Helper()
	fsys := afero.NewMemMapFs()
	const dir = "/proj/content/bundles"
	require.NoError(t, afero.WriteFile(fsys, dir+"/"+rel, []byte(doc), 0o644))
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
	read := readOne(t, projectReaderOver(t, "onpath.yaml", "version: 1.0.0\nname: declared\n"))

	assert.Equal(t, "declared", read.Bundle.Name, "a declared name must win over the path-derived one")
	assert.Equal(t, "onpath", read.DisplayName(), "the resolution ref stays path-derived; only Name is declared")
}

// TestProjectReader_UndeclaredNameFallsBackToPath is the other half of the same
// rule, and the one that keeps 69 existing bundle files working: a bundle that
// declares nothing still resolves under the name its path implies.
func TestProjectReader_UndeclaredNameFallsBackToPath(t *testing.T) {
	read := readOne(t, projectReaderOver(t, "onpath.yaml", "version: 1.0.0\n"))

	assert.Equal(t, "onpath", read.Bundle.Name, "a bundle declaring no name falls back to its path-derived name")
}

// TestNewRepoFSReader_DeclaredNameWinsOverTheCanonicalRef applies the same rule
// to pinned remote content. The canonical ref stays the SOLE resolution
// identity — it is what a profile authors and what sourceRef keys trust by — so
// the test asserts the ref is unmoved while the declared name lands.
func TestNewRepoFSReader_DeclaredNameWinsOverTheCanonicalRef(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	doc := []byte("version: \"1.0\"\nname: declared\n")
	tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": doc})
	require.NoError(t, err)

	reads, err := NewRepoFSReader(tree, ref).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, "declared", reads[0].Bundle.Name, "a declared name must win over the canonical ref")
	assert.Equal(t, ref, reads[0].DisplayName(), "canonical stays the sole resolution identity")
}

// TestNewRepoFSReader_UndeclaredNameFallsBackToTheCanonicalRef is the other half:
// nothing that declares no name changes behaviour.
func TestNewRepoFSReader_UndeclaredNameFallsBackToTheCanonicalRef(t *testing.T) {
	const ref = "https://example.test/repo@bundles/kit"
	tree, err := content.NewMapTreeFS(map[string][]byte{"kit.yaml": readerBundleYAML})
	require.NoError(t, err)

	reads, err := NewRepoFSReader(tree, ref).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, ref, reads[0].Bundle.Name, "an undeclared remote bundle still falls back to its canonical ref")
}

// TestNewCompanionReader_DeclaredNameWinsOverTheCompanionRef applies the rule to
// a companion loadout, whose ctxloom:companion@<bin> ref is likewise the
// resolution identity and only the fallback for Name.
func TestNewCompanionReader_DeclaredNameWinsOverTheCompanionRef(t *testing.T) {
	probe := loadoutProbe(CompanionLoadout{Bin: "ltk", Bundle: []byte("version: \"1.0\"\nname: declared\n")})

	reads, err := NewCompanionReader(probe).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, "declared", reads[0].Bundle.Name, "a declared name must win over the companion ref")
	assert.Equal(t, companionRefPrefix+"ltk", reads[0].DisplayName(), "the companion ref stays the resolution identity")
}

// TestNewCompanionReader_UndeclaredNameFallsBackToTheCompanionRef is the other
// half for companions.
func TestNewCompanionReader_UndeclaredNameFallsBackToTheCompanionRef(t *testing.T) {
	probe := loadoutProbe(CompanionLoadout{Bin: "ltk", Bundle: readerBundleYAML})

	reads, err := NewCompanionReader(probe).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)

	assert.Equal(t, companionRefPrefix+"ltk", reads[0].Bundle.Name,
		"an undeclared companion loadout still falls back to its companion ref")
}
