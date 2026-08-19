package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// coexistFS is a project tree holding one bundle named after the one bundle
// this binary actually embeds, so both source classes ship the same declared
// name.
func coexistFS(t *testing.T, name string) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bundles/"+name+".yaml",
		[]byte("version: 1.0.0\ndescription: the PROJECT one\n"), 0o644))
	return fs
}

// TestCatalog_SameDeclaredNameDifferentClasses_BothResolve is the property the
// merged namespace exists for: a declared name cannot displace anything,
// because it is not what a bundle is keyed by.
//
// Two bundles named "isolation" — one written in the project, one compiled
// into this binary — are two entries in one map, under two canonical URIs.
// Both are listed, both are loadable, both are independently trustable. There
// is no winner to pick and nothing to announce.
//
// What the bare name costs is that it stops naming exactly one bundle, and
// that is answered by REFUSING it rather than by choosing for the user. A
// refusal delivers nothing; any winner-picking rule delivers content the user
// did not ask for and cannot see was substituted.
func TestCatalog_SameDeclaredNameDifferentClasses_BothResolve(t *testing.T) {
	const shared = "isolation"

	cat := NewLoader(
		NewProjectReader(coexistFS(t, shared), []string{"/bundles"}),
		NewBuiltinReader(),
	).Catalog()

	require.Equal(t, 2, cat.Len(),
		"two bundles of one declared name under two canonical URIs are two entries; "+
			"a set that holds one has keyed on the name and silently dropped a bundle")

	local, err := trust.LocalRef(shared)
	require.NoError(t, err)
	builtin, err := trust.BuiltinRef(shared)
	require.NoError(t, err)

	projectRead, ok := cat.LookupKey(local.BundleIdentity())
	require.True(t, ok, "the project copy must resolve by its canonical URI")
	builtinRead, ok := cat.LookupKey(builtin.BundleIdentity())
	require.True(t, ok, "the builtin copy must resolve by its canonical URI")

	assert.NotEqual(t, projectRead.Key(), builtinRead.Key(),
		"the two URIs must resolve to DIFFERENT reads; one read answering both is the shadowing this removes")
	assert.Equal(t, "the PROJECT one", projectRead.Bundle.Description)
	assert.NotEqual(t, "the PROJECT one", builtinRead.Bundle.Description)

	_, err = cat.Lookup(shared)
	require.Error(t, err, "a name that means two bundles must resolve to NOTHING, not to a winner")
	assert.ErrorIs(t, err, errs.ErrBundleAmbiguous)
	assert.Contains(t, err.Error(), string(local.BundleIdentity()),
		"the refusal must name the project candidate's URI — a user cannot say which they meant otherwise")
	assert.Contains(t, err.Error(), string(builtin.BundleIdentity()),
		"the refusal must name the builtin candidate's URI")
}

// TestCatalogFS_StaysTheProjectFilesystem guards the ordering that made
// composition order dangerous in the first place.
//
// Loader.FS() returns the FIRST reader that has a filesystem, and the builtin
// reader has one — the EMBEDDED fs. Composing it ahead of the project reader
// therefore makes FS() report the embedded filesystem, and since a skill's
// trust preimage is derived from the tree at that fs, every project skill
// hashes against a tree that does not exist there and is silently withheld.
// Composition order no longer decides IDENTITY, but it still decides this.
func TestCatalogFS_StaysTheProjectFilesystem(t *testing.T) {
	fs := coexistFS(t, "isolation")
	l := NewLoader(NewProjectReader(fs, []string{"/bundles"}), NewBuiltinReader())

	require.Same(t, fs, l.FS(),
		"a loader carrying the builtin reader must still read skills from the PROJECT filesystem; "+
			"reader order alone can redirect it to the embedded one and silently withhold every skill")
}

// TestUnshadowedBuiltin_ResolvesByItsBareName is the cost the ambiguity
// refusal must not overcharge: a builtin no other bundle shares a name with
// still resolves by its plain name.
func TestUnshadowedBuiltin_ResolvesByItsBareName(t *testing.T) {
	b, err := NewLoader(NewBuiltinReader()).Load("isolation")
	require.NoError(t, err,
		"with no other bundle of that name the bare name must resolve to the builtin — "+
			"refusing an AMBIGUOUS name must not make unambiguous ones unreachable")
	assert.NotEqual(t, "the PROJECT one", b.Description)
}
