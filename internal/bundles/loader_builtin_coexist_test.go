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

	// Builtin first so that, on a bare ask, "later reader wins" would hand back
	// the project bundle even without the qualified ref. The point of the
	// assertions below is that BOTH survive indexing — a shared key would have
	// discarded one outright, whatever the order.
	l := NewLoader(NewBuiltinReader(), NewProjectReader(fs, []string{"/bundles"}))

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

	t.Run("a bare ask still reaches a builtin nothing shadows", func(t *testing.T) {
		bare := NewLoader(NewBuiltinReader())
		b, err := bare.Load(shared)
		require.NoError(t, err,
			"with no project bundle of that name, the bare name must still resolve to the builtin — "+
				"qualifying the ref must not make builtins unreachable by their plain name")
		require.NotEqual(t, "the PROJECT one", b.Description)
	})
}
