package schemaver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// --- Declared -----------------------------------------------------------

func TestDeclared_NeitherKeyPresent_IsGenerationZero(t *testing.T) {
	v, ok := Declared([]byte("kept: 1\n"))
	assert.True(t, ok, "a document with no version key at all is the pre-versioning generation")
	assert.Equal(t, 0, v)
}

func TestDeclared_KeyPresent_ReadsInteger(t *testing.T) {
	v, ok := Declared([]byte("schema_version: 7\nkept: 1\n"))
	require.True(t, ok)
	assert.Equal(t, 7, v)
}

func TestDeclared_FallsBackToLegacyKey(t *testing.T) {
	v, ok := Declared([]byte("version: 4\nkept: 1\n"))
	require.True(t, ok)
	assert.Equal(t, 4, v)
}

func TestDeclared_KeyPreferredOverLegacyKey(t *testing.T) {
	v, ok := Declared([]byte("schema_version: 9\nversion: 2\n"))
	require.True(t, ok)
	assert.Equal(t, 9, v, "Key must win when both are present")
}

func TestDeclared_PresentButUnreadable(t *testing.T) {
	cases := map[string]string{
		"non-integer word":   "schema_version: banana\nkept: 1\n",
		"float":              "schema_version: 6.5\nkept: 1\n",
		"empty string":       "schema_version: \"\"\nkept: 1\n",
		"nested mapping":     "schema_version:\n  nested: 6\n",
		"sequence":           "schema_version:\n  - 6\n",
		"legacy non-integer": "version: banana\nkept: 1\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			v, ok := Declared([]byte(doc))
			assert.False(t, ok, "input %q must not read as a genuine version", doc)
			assert.Equal(t, 0, v)
		})
	}
}

func TestDeclared_MalformedYAML_IsUnreadable(t *testing.T) {
	v, ok := Declared([]byte("key: [unterminated\n"))
	assert.False(t, ok)
	assert.Equal(t, 0, v)
}

func TestDeclared_MultiDocument_IsUnreadable(t *testing.T) {
	v, ok := Declared([]byte("schema_version: 3\n---\nsecond: doc\n"))
	assert.False(t, ok)
	assert.Equal(t, 0, v)
}

func TestDeclared_NonMappingDocument_IsUnreadable(t *testing.T) {
	v, ok := Declared([]byte("- a\n- b\n"))
	assert.False(t, ok)
	assert.Equal(t, 0, v)
}

// --- RenameUpgrade --------------------------------------------------------

func TestRenameUpgrade_AlreadyAtKey_PassesThroughByteIdentical(t *testing.T) {
	in := []byte("schema_version: 3\nkept: 1\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
	assert.Same(t, &in[0], &out[0], "already-current input must not be reserialized")
}

func TestRenameUpgrade_LegacyKey_RenamedPreservingCommentsAndIndent(t *testing.T) {
	// version's own head comment sits on the sibling "kept" key instead,
	// matching upgrade_test.go's TestPipeline_Run_PreservesCommentsAndIndent:
	// a renamed key's own head comment is not guaranteed to survive, but
	// comments on untouched structure around it must.
	in := []byte("version: 5\nnested:\n  # nested comment\n  a: 1\n# kept comment\nkept: 1\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	require.Equal(t, []string{"rename version to schema_version"}, applied)

	s := string(out)
	assert.Contains(t, s, "# nested comment")
	assert.Contains(t, s, "# kept comment")
	assert.Contains(t, s, "schema_version: 5")
	assert.NotContains(t, s, "\nversion: 5", "the legacy top-level key must be gone, not just shadowed by schema_version containing \"version\" as a substring")
	assert.Contains(t, s, "  a: 1", "nested indentation must survive the round trip")

	v, ok := Declared(out)
	require.True(t, ok)
	assert.Equal(t, 5, v, "the renamed value must be the same integer")
}

func TestRenameUpgrade_BothKeysPresent_LeftUntouched(t *testing.T) {
	in := []byte("version: 1\nschema_version: 2\nkept: x\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	assert.Empty(t, applied, "a document with both keys must not be silently resolved")
	assert.Equal(t, in, out)
}

// Neither key present must be a true no-op: RenameUpgrade must not reach for
// a nil legacy value and stamp it onto Key. (A mutant that dropped the
// legacy==nil early return survived every other test in this file because
// every other test's fixture has at least one of the two keys present; this
// is the one case that actually exercises "neither present".)
func TestRenameUpgrade_NeitherKeyPresent_NoOp(t *testing.T) {
	in := []byte("kept: 1\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
}

func TestRenameUpgrade_Idempotent_SecondRunIsNoOp(t *testing.T) {
	p := upgrade.Pipeline{RenameUpgrade()}
	once, applied := p.Run([]byte("version: 8\n"))
	require.NotEmpty(t, applied)
	twice, appliedAgain := p.Run(once)
	assert.Empty(t, appliedAgain, "already-renamed input must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

func TestRenameUpgrade_MalformedYAML_ReturnsVerbatim(t *testing.T) {
	in := []byte("version: [unterminated\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
}

func TestRenameUpgrade_MultiDocumentStream_ReturnsVerbatim(t *testing.T) {
	in := []byte("version: 1\n---\nsecond: doc\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	assert.Empty(t, applied, "a multi-document stream must not be reported as upgraded")
	assert.Equal(t, string(in), string(out), "every document in the stream must survive")
}

func TestRenameUpgrade_DuplicateKey_ReturnsVerbatim(t *testing.T) {
	in := []byte("version: 1\nversion: 2\n")
	p := upgrade.Pipeline{RenameUpgrade()}
	out, applied := p.Run(in)
	assert.Empty(t, applied, "a duplicate key document must not be silently normalized")
	assert.Equal(t, string(in), string(out))
}

// --- Kind -----------------------------------------------------------------

// The steady state: a Kind with zero registered migrations must still work
// correctly end to end, because that is exactly the state every real file
// kind is in until its first migration is registered.
func TestKind_Migrate_EmptyUpgrades_PassesThroughVerbatim(t *testing.T) {
	k := Kind{Name: "widget", Current: 1, Upgrades: upgrade.Pipeline{}}
	in := []byte("version: 1\nkept: x\n")
	out, applied := k.Migrate(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
	assert.Same(t, &in[0], &out[0], "an empty pipeline must not reserialize")
}

func TestKind_Migrate_NilUpgrades_PassesThroughVerbatim(t *testing.T) {
	k := Kind{Name: "widget", Current: 1}
	in := []byte("kept: x\n")
	out, applied := k.Migrate(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
}

// Proves the composition a real call site will use: a Kind whose Upgrades
// lists RenameUpgrade first performs the rename via Migrate, demonstrating
// the "one-line registration against proven machinery" the design promises.
func TestKind_Migrate_ComposesRegisteredUpgrades(t *testing.T) {
	k := Kind{
		Name:     "widget",
		Current:  1,
		Upgrades: upgrade.Pipeline{RenameUpgrade()},
	}
	out, applied := k.Migrate([]byte("version: 2\nkept: x\n"))
	require.Equal(t, []string{"rename version to schema_version"}, applied)
	v, ok := Declared(out)
	require.True(t, ok)
	assert.Equal(t, 2, v)
}
