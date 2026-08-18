package bundles

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBundleWarner_DedupesPerRef verifies a ref warns once even across repeated
// calls (startup assembles context more than once via independent loaders).
func TestBundleWarner_DedupesPerRef(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner()
	err := errors.New("bundle not found")

	w.unresolved(&buf, "personal/core-practices", err)
	w.unresolved(&buf, "personal/core-practices", err)
	w.unresolved(&buf, "personal/developer-mindset", err)

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, `bundle "personal/core-practices"`), "repeated ref must warn once")
	assert.Equal(t, 1, strings.Count(out, `bundle "personal/developer-mindset"`))
	assert.Equal(t, 2, strings.Count(out, "skipping unresolved bundle"))
}

// TestBundleWarner_UnresolvedAndAmbiguousDoNotShareAKeyspace pins the fix below.
//
// One `seen` map served two kinds of warning asymmetrically: `unresolved` keyed
// on the bare ref, `ambiguous` keyed on the string "ambiguous:"+name. A bundle
// ref literally named "ambiguous:foo" therefore occupied the same key as the
// ambiguity warning for fragment "foo", and whichever fired first silenced the
// other. String-prefixed namespacing inside a shared keyspace is a collision
// waiting for the right name; a typed key has no such shape.
func TestBundleWarner_UnresolvedAndAmbiguousDoNotShareAKeyspace(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner()

	w.unresolved(&buf, "ambiguous:foo", errors.New("bundle not found"))
	w.ambiguous(&buf, "foo", []string{"a", "b"}, "a")

	out := buf.String()
	assert.Contains(t, out, "skipping unresolved bundle", "the unresolved-bundle warning must be emitted")
	assert.Contains(t, out, "exists in multiple bundles",
		"the ambiguity warning must not be suppressed by an unrelated bundle ref that merely spells its dedup key")
}

// TestBundleWarner_AmbiguousDedupesPerName pins that collapsing the two
// keyspaces did not cost the ambiguous warning its own dedup.
func TestBundleWarner_AmbiguousDedupesPerName(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner()

	w.ambiguous(&buf, "shared", []string{"a", "b"}, "a")
	w.ambiguous(&buf, "shared", []string{"a", "b"}, "a")
	w.ambiguous(&buf, "other", []string{"a", "b"}, "a")

	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, "exists in multiple bundles"))
	assert.Equal(t, 1, strings.Count(out, `fragment "shared"`))
}

// TestLoader_WarnWriterReceivesTheWarnerDiagnostics pins the fix below.
//
// WithWarnWriter's contract is "redirects THIS loader/store's user-facing
// diagnostics (the clidiag 'ctxloom: warning:' lines)". It did not: only
// fsStore.Save's signature warning honoured it, while the loader's two other
// user-facing warnings — an unresolved bundle ref and an ambiguous bare
// fragment ask — went to a process-global sink hardwired to os.Stderr.
//
// So a caller that redirected diagnostics still got them on stderr (a real
// problem for `--format json`, whose contract is that stderr is the only
// diagnostic channel and the caller decides where it goes), and a test that
// captured the warn writer read silence as "nothing was wrong" — the exact
// misreading these diagnostics exist to prevent.
func TestLoader_WarnWriterReceivesTheWarnerDiagnostics(t *testing.T) {
	t.Run("unresolved bundle ref", func(t *testing.T) {
		// Reset the process-wide warner. It dedups per ref for the life of the
		// process, so without this the assertion below holds only on the FIRST
		// run in a process: `go test -count=2` (and any harness that re-executes
		// a test function) sees the second occurrence deduped away and reads the
		// silence as "no diagnostic emitted". Same reason captureBundleWarner
		// exists in loader_silent_failure_test.go; this test never adopted it.
		_ = captureBundleWarner(t)

		var warnings strings.Builder
		l := NewLoader(NewProjectReader(afero.NewMemMapFs(), nil)).WithWarnWriter(&warnings)

		got := ungated(l, false).CommandsFromBundleRef("u031-f14-unresolved-ref")

		require.Empty(t, got)
		assert.Contains(t, warnings.String(), "u031-f14-unresolved-ref",
			"a loader told where to put its diagnostics must put ALL of them there")
	})

	t.Run("ambiguous bare fragment ask", func(t *testing.T) {
		_ = captureBundleWarner(t) // see the sibling subtest

		fsys := afero.NewMemMapFs()
		dir := "/bundles"
		require.NoError(t, afero.WriteFile(fsys, dir+"/alpha.yaml",
			[]byte("version: \"1.0\"\nfragments:\n  u031f14shared:\n    content: a\n"), 0o644))
		require.NoError(t, afero.WriteFile(fsys, dir+"/beta.yaml",
			[]byte("version: \"1.0\"\nfragments:\n  u031f14shared:\n    content: b\n"), 0o644))

		var warnings strings.Builder
		l := NewLoader(NewProjectReader(fsys, []string{dir})).WithWarnWriter(&warnings)

		resolved := l.ResolveFragmentAsk("u031f14shared")

		assert.Contains(t, resolved, "u031f14shared", "the ask still resolves deterministically")
		assert.Contains(t, warnings.String(), "exists in multiple bundles",
			"the ambiguity warning must reach the loader's own warn writer, not a process-global stderr")
	})
}
