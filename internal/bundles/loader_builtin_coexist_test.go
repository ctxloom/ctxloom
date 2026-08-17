package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestBuiltinAndProjectBundleOfOneName_Coexist pins the resolution rule for a
// project bundle that shares a builtin's name.
//
// Both are read by the SAME localFSReader, so both used to mint a bare
// path-relative ref. Composed into one loader — which is exactly what admitting
// the builtin reader to Config.BundleLoader does — they collided on one map key
// and whichever reader ran last silently won, making the other unreachable with
// no diagnostic.
//
// The rule now: both are present and addressable by their qualified refs, and a
// BARE ask resolves to the project's, because naming a bundle after a builtin is
// a deliberate override rather than an error.
func TestBuiltinAndProjectBundleOfOneName_Coexist(t *testing.T) {
	const shared = "isolation" // the one bundle this binary actually embeds

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bundles/"+shared+".yaml",
		[]byte("version: 1.0.0\ndescription: the PROJECT one\n"), 0o644))

	// Project reader FIRST — the production order, and load-bearing: see the
	// FS() subtest below. The assertions here are that BOTH survive indexing,
	// which a shared resolution key would have prevented whatever the order.
	l := NewLoader(NewProjectReader(fs, []string{"/bundles"}), NewBuiltinReader())

	t.Run("both survive indexing", func(t *testing.T) {
		var refs []string
		for _, r := range l.Reads() {
			refs = append(refs, r.Ref())
		}
		require.Contains(t, refs, trust.BuiltinSourcePrefix+shared,
			"the builtin must remain addressable; sharing a resolution key discarded it")
		require.Contains(t, refs, shared,
			"the project bundle must remain addressable")
	})

	t.Run("the qualified ref addresses the builtin exactly", func(t *testing.T) {
		b, err := l.Load(trust.BuiltinSourcePrefix + shared)
		require.NoError(t, err)
		require.NotEqual(t, "the PROJECT one", b.Description,
			"builtin:<name> must reach the BUILTIN, not whichever bundle happens to share the name")
	})

	t.Run("a bare ask prefers the project bundle", func(t *testing.T) {
		b, err := l.Load(shared)
		require.NoError(t, err)
		require.Equal(t, "the PROJECT one", b.Description,
			"naming a bundle after a builtin is an override: the bare name keeps resolving to the project's")
	})

	t.Run("FS stays the project filesystem, not the embedded one", func(t *testing.T) {
		// Loader.FS() returns the FIRST reader that has a filesystem, and the
		// builtin reader has one — the EMBEDDED fs. Composing it ahead of the
		// project reader therefore makes FS() report the embedded filesystem,
		// and since a skill's trust preimage is derived from the tree at that
		// fs, every project skill hashes against a tree that does not exist
		// there and is silently withheld. Measured, not hypothesised: it broke
		// two skill-resolution tests the moment the builtin reader was listed
		// first.
		require.Same(t, fs, l.FS(),
			"a loader carrying the builtin reader must still read skills from the PROJECT filesystem; "+
				"reader order alone can redirect it to the embedded one and silently withhold every skill")
	})

	t.Run("a bare ask still reaches a builtin nothing shadows", func(t *testing.T) {
		bare := NewLoader(NewBuiltinReader())
		b, err := bare.Load(shared)
		require.NoError(t, err,
			"with no project bundle of that name, the bare name must still resolve to the builtin — "+
				"qualifying the ref must not make builtins unreachable by their plain name")
		require.NotEqual(t, "the PROJECT one", b.Description)
	})
}
