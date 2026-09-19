package bundles

import (
	"errors"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBundleWarner_DedupesPerRef verifies a ref warns once even across repeated
// calls (startup assembles context more than once via independent loaders).
// The unresolved-bundle and ambiguous-fragment diagnostics are Once findings:
// whether one has already been said is the sink's business (strictness.Sink
// dedups by text), so a Catalog reports every occurrence and marks it.
func TestCatalog_UnresolvedBundleIsAOnceFinding(t *testing.T) {
	var found report.Collector
	c := Catalog{}.WithReporter(&found)
	err := errors.New("bundle not found")

	c.warnUnresolvedBundle("personal/core-practices", err)
	c.warnUnresolvedBundle("personal/developer-mindset", err)

	got := found.All()
	require.Len(t, got, 2)
	for _, f := range got {
		assert.True(t, f.Once, "a repeated ref must render once")
		assert.False(t, f.Fatal(), "an unresolved ref is advisory, not a startup refusal")
		assert.Contains(t, f.Text, "skipping unresolved bundle")
	}
	assert.Contains(t, got[0].Text, `bundle "personal/core-practices"`)
	assert.Contains(t, got[1].Text, `bundle "personal/developer-mindset"`)
}

func TestCatalog_AmbiguousFragmentIsAOnceFinding(t *testing.T) {
	var found report.Collector
	c := Catalog{}.WithReporter(&found)

	c.warnAmbiguousFragment("shared", []string{"a", "b"}, "a")

	got := found.All()
	require.Len(t, got, 1)
	assert.True(t, got[0].Once)
	assert.Contains(t, got[0].Text, `fragment "shared" exists in multiple bundles (a, b); using a`)
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
func TestLoader_ReporterReceivesTheCatalogDiagnostics(t *testing.T) {
	t.Run("unresolved bundle ref", func(t *testing.T) {
		// Reset the process-wide warner. It dedups per ref for the life of the
		// process, so without this the assertion below holds only on the FIRST
		// run in a process: `go test -count=2` (and any harness that re-executes
		// a test function) sees the second occurrence deduped away and reads the
		// silence as "no diagnostic emitted". Same reason captureBundleWarner
		// exists in loader_silent_failure_test.go; this test never adopted it.
		_ = captureBundleWarner(t)

		var warnings findingLines
		l := NewLoader(NewProjectReader(afero.NewMemMapFs(), nil)).WithReporter(&warnings)

		got := ungated(l, false).CommandsFromBundleRef("u031-f14-unresolved-ref")

		require.Empty(t, got)
		assert.Contains(t, warnings.String(), "u031-f14-unresolved-ref",
			"a loader told where to put its diagnostics must put ALL of them there")
	})

	t.Run("ambiguous bare fragment ask", func(t *testing.T) {
		_ = captureBundleWarner(t) // see the sibling subtest

		fsys := afero.NewMemMapFs()
		dir := "/bundles"
		require.NoError(t, afero.WriteFile(fsys, bundlesRootIn(dir, "alpha.yaml"),
			[]byte("version: \"1.0\"\nfragments:\n  u031f14shared:\n    content: a\n"), 0o644))
		require.NoError(t, afero.WriteFile(fsys, bundlesRootIn(dir, "beta.yaml"),
			[]byte("version: \"1.0\"\nfragments:\n  u031f14shared:\n    content: b\n"), 0o644))

		var warnings findingLines
		l := NewLoader(NewProjectReader(fsys, []string{dir})).WithReporter(&warnings)

		resolved := l.ResolveFragmentAsk("u031f14shared")

		assert.Contains(t, resolved, "u031f14shared", "the ask still resolves deterministically")
		assert.Contains(t, warnings.String(), "exists in multiple bundles",
			"the ambiguity warning must reach the loader's own warn writer, not a process-global stderr")
	})
}
