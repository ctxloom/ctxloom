package bundles

import (
	"bytes"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestBuiltinAndProjectBundleOfOneName_ProjectShadowsBuiltin pins the collision
// rule for a project bundle that shares a builtin's name.
//
// S1a dodged this collision by minting a builtin's resolution ref as
// "builtin:<name>", so the two were simply different map keys. That put the
// SOURCE CLASS into the resolution identity, which is the rule this package is
// not allowed to break — a bundle is addressed by what it declares, not by
// where it sits — and it leaked "builtin:" into every listing that showed a
// ref. I7 removed the prefix, so both now key to the same bare name and the
// collision is real.
//
// The decided rule (human, 2026-08-17):
//
//   - the PROJECT bundle WINS; naming a bundle after a builtin is a deliberate
//     override, and the builtin is shadowed;
//   - the shadowing is ANNOUNCED — a bundle-class finding naming BOTH refs and
//     saying to rename one;
//   - the session PROCEEDS. Refusal was considered and rejected: a bundle
//     published upstream later adopting a name a project already uses would
//     otherwise break that project on its next `deps pull`, over a clash the
//     user did not create. Silent precedence was rejected too — it is this
//     project's characteristic silent-no-op shape.
//
// Announcing is the half most easily lost: shadowing that works but says
// nothing is indistinguishable from the bug it replaced.
func TestBuiltinAndProjectBundleOfOneName_ProjectShadowsBuiltin(t *testing.T) {
	const shared = "isolation" // the one bundle this binary actually embeds

	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(strictness.Reset)
	// FailOnce's line dedup is process-wide and permanent, so without this the
	// sink assertion below is only meaningful on the first run in a process
	// (`go test -count=2` would see silence and read it as success).
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bundles/"+shared+".yaml",
		[]byte("version: 1.0.0\ndescription: the PROJECT one\n"), 0o644))

	// Project reader FIRST — the production order, and load-bearing for the
	// FS() subtest below. The shadowing rule must not depend on that order,
	// which is what the reversed-order subtest checks.
	l := NewLoader(NewProjectReader(fs, []string{"/bundles"}), NewBuiltinReader())

	mark := strictness.Checkpoint()
	reads := l.Reads()

	t.Run("the project bundle wins the shared name", func(t *testing.T) {
		b, err := l.Load(shared)
		require.NoError(t, err)
		assert.Equal(t, "the PROJECT one", b.Description,
			"naming a bundle after a builtin is an override: the bare name resolves to the project's")
	})

	t.Run("the builtin is shadowed, not merely deprioritised", func(t *testing.T) {
		var refs []string
		for _, r := range reads {
			refs = append(refs, r.Ref())
		}
		assert.Contains(t, refs, shared, "the winning bundle must be addressable")
		assert.NotContains(t, refs, trust.BuiltinSourcePrefix+shared,
			"\"builtin:<name>\" is a TRUST ref, never a listing handle: a resolution ref must not carry a source class")

		_, err := l.Load(trust.BuiltinSourcePrefix + shared)
		assert.Error(t, err,
			"the shadowed builtin has no back-door handle; that is what makes the announcement load-bearing")

		// Exactly one bundle answers to the shared name — a shadowing that
		// left two entries behind would resolve non-deterministically.
		var atShared int
		for _, r := range reads {
			if r.Ref() == shared {
				atShared++
			}
		}
		assert.Equal(t, 1, atShared, "the collision must collapse to one read, not two")
	})

	t.Run("the shadowing is announced, naming both refs and the fix", func(t *testing.T) {
		findings := strictness.Since(mark)
		require.Len(t, findings, 1, "shadowing a builtin must be reported, not silent")
		assert.Equal(t, strictness.ClassBundle, findings[0].Class)
		assert.Contains(t, findings[0].Message, shared, "the finding names the contested name")
		assert.Contains(t, findings[0].Message, trust.BuiltinSourcePrefix+shared,
			"the finding names the SHADOWED bundle by its trust ref — otherwise the user cannot tell which lost")

		assert.Contains(t, findings[0].FixIt, "rename",
			"the finding must carry the fix: rename one of the two bundles")

		assert.Contains(t, sink.String(), trust.BuiltinSourcePrefix+shared,
			"the user must actually SEE the shadowing streamed, not only a recorded finding")
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
}

// TestBuiltinShadowing_IsNotReaderOrderLuck is the other half of the rule. A
// loader resolves a plain name collision by LAST READER WINS — deliberately,
// so pinned remote content shadows a stale extracted copy on disk. If the
// builtin rule were left to that default it would hold only for the one reader
// order production happens to use today, and reversing the composition would
// silently hand the name back to the builtin.
func TestBuiltinShadowing_IsNotReaderOrderLuck(t *testing.T) {
	const shared = "isolation"

	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(strictness.Reset)

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bundles/"+shared+".yaml",
		[]byte("version: 1.0.0\ndescription: the PROJECT one\n"), 0o644))

	// BUILTIN reader last — the order under which last-wins alone would give
	// the builtin the name.
	b, err := NewLoader(NewProjectReader(fs, []string{"/bundles"}), NewBuiltinReader()).Load(shared)
	require.NoError(t, err)
	require.Equal(t, "the PROJECT one", b.Description)

	// PROJECT reader last: the same outcome must hold, reached by the other
	// branch of the rule.
	b, err = NewLoader(NewBuiltinReader(), NewProjectReader(fs, []string{"/bundles"})).Load(shared)
	require.NoError(t, err)
	assert.Equal(t, "the PROJECT one", b.Description,
		"a builtin must never displace a project bundle in EITHER reader order — "+
			"a rule that only holds for today's composition is not a rule")
}

// TestUnshadowedBuiltin_ResolvesByItsBareName guards the cost of the rule
// above: qualification left the resolution ref, so a builtin nothing shadows
// must still be reachable, and reachable by its PLAIN name.
func TestUnshadowedBuiltin_ResolvesByItsBareName(t *testing.T) {
	b, err := NewLoader(NewBuiltinReader()).Load("isolation")
	require.NoError(t, err,
		"with no project bundle of that name the bare name must resolve to the builtin — "+
			"dropping the qualification must not make builtins unreachable")
	assert.NotEqual(t, "the PROJECT one", b.Description)
}
