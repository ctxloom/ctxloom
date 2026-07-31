package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// U048-F21: strandedAuthoredBundles discarded the afero.Walk error and its
// per-entry callback returned nil on ANY error, so a directory it could not
// enumerate silently contributed nothing.
//
// That is not a cosmetic swallow, because it inverts the function's own stated
// contract: "Unreadable/unparseable files are treated as authored: a file we
// cannot prove is regenerable cache is work we must not tell the user to
// ignore." An unreadable subtree is the strongest possible case of "cannot
// prove", and it was the one case that came back clean — so the signpost that
// tells a user to move their authored bundles out of the gitignored cache would
// say nothing at all, and the bundles would stay invisible to `bundle list`,
// `run` and `sign --all` with no explanation.
//
// The bias is deliberately toward OVER-reporting: the cost of naming a
// directory we could not read is a fix-it the user does not need, and the cost
// of staying quiet is their own work disappearing.

// TestStrandedAuthoredBundles_UnreadableSubtreeIsReported is the row's case.
func TestStrandedAuthoredBundles_UnreadableSubtreeIsReported(t *testing.T) {
	mem := afero.NewMemMapFs()
	cacheBundles := paths.CacheBundlesPath("/app")
	locked := filepath.Join(cacheBundles, "locked")
	require.NoError(t, mem.MkdirAll(locked, 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(locked, "mine.yaml"),
		[]byte("name: mine\n"), 0o644))
	// A readable sibling, so the assertion cannot pass merely because the whole
	// scan collapsed.
	require.NoError(t, afero.WriteFile(mem, filepath.Join(cacheBundles, "visible.yaml"),
		[]byte("name: visible\n"), 0o644))

	readable := strandedAuthoredBundles(mem, cacheBundles)
	require.ElementsMatch(t, []string{"locked/mine.yaml", "visible.yaml"}, readable,
		"with a readable tree both authored bundles are found — the fixture is sound")

	got := strandedAuthoredBundles(denyOpenFs{Fs: mem, deny: locked}, cacheBundles)

	assert.Contains(t, got, "visible.yaml", "the readable half is unaffected")
	assert.Contains(t, got, "locked",
		"a directory whose contents cannot be read hides an unknown number of authored bundles; reporting nothing for it is the under-report the doc forbids")
}

// TestStrandedAuthoredBundles_UnreadableYAMLIsStillAuthored covers the entry-
// level half of the same contract, which the ReadFile branch already honoured
// and which must not regress: a .yaml we cannot open cannot be shown to carry
// the _source.sha marker that identifies regenerable cache, so it counts.
func TestStrandedAuthoredBundles_UnreadableYAMLIsStillAuthored(t *testing.T) {
	mem := afero.NewMemMapFs()
	cacheBundles := paths.CacheBundlesPath("/app")
	require.NoError(t, mem.MkdirAll(cacheBundles, 0o755))
	target := filepath.Join(cacheBundles, "unreadable.yaml")
	require.NoError(t, afero.WriteFile(mem, target, []byte("name: x\n"), 0o644))

	got := strandedAuthoredBundles(denyOpenFs{Fs: mem, deny: target}, cacheBundles)

	assert.Equal(t, []string{"unreadable.yaml"}, got)
}

// TestStrandedAuthoredBundles_RemotePullArtifactsAreStillCache pins the
// behaviour the over-report bias must NOT trample: a file carrying the
// _source.sha marker is regenerable cache and stays out of the list.
func TestStrandedAuthoredBundles_RemotePullArtifactsAreStillCache(t *testing.T) {
	mem := afero.NewMemMapFs()
	cacheBundles := paths.CacheBundlesPath("/app")
	require.NoError(t, mem.MkdirAll(cacheBundles, 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(cacheBundles, "pulled.yaml"),
		[]byte("name: pulled\n_source:\n  sha: deadbeef\n"), 0o644))

	assert.Empty(t, strandedAuthoredBundles(mem, cacheBundles))
}
