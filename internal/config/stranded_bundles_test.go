package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

// countingFs counts Open calls, which is what afero.Walk uses to enumerate a
// directory (readDirNames). It is how the pin below distinguishes "the scan
// ran" from "the scan was skipped".
type countingFs struct {
	afero.Fs
	opens int
}

func (f *countingFs) Open(name string) (afero.File, error) {
	f.opens++
	return f.Fs.Open(name)
}

// U048-F07 claimed GetBundleDirs — "called many times per process (every loader
// build)" by its own doc — runs a full afero.Walk of cache/bundles with a
// ReadFile plus yaml.Unmarshal per YAML on EVERY call.
//
// MEASURED, and the "every call" is conditional in a way that changes the
// verdict: strandedAuthoredBundles opens with fs.Stat(cacheBundles) and returns
// immediately when the directory is absent or is not a directory. cache/bundles
// is a PRE-content-tree migration artifact — nothing in the current codebase
// creates it — so on a current install every GetBundleDirs call costs one Stat
// and walks nothing at all.
//
// Where it does exist, the repetition is required rather than wasteful. The
// signpost reports through strictness.FailOnce, whose finding dedup is scoped
// to the recording goroutine's CURRENT checkpoint window precisely so a fault
// re-fired in a LATER window records again — "a session refused over it and
// retried unfixed opens silently on broken context", per FailOnce's own doc.
// Memoizing the scan would silently defeat that, which makes the obvious remedy
// inadmissible: it would trade a cost paid only by users mid-migration for a
// contract this codebase names and relies on.
//
// So the pin is on the guard, which is the part that must not regress: moving
// the walk ahead of the Stat, or dropping the Stat, would make the row's claim
// true for everyone.
func TestGetBundleDirs_DoesNotWalkWhenNoLegacyCacheExists(t *testing.T) {
	mem := afero.NewMemMapFs()
	appPath := "/app"
	require.NoError(t, mem.MkdirAll(paths.LocalBundlesPath(appPath), 0o755))
	counting := &countingFs{Fs: mem}

	cfg := NewFixture(Fixture{AppPaths: []string{appPath}})
	cfg.SetFS(counting)

	for range 5 {
		require.Equal(t, []string{paths.LocalBundlesPath(appPath)}, cfg.GetBundleDirs())
	}

	assert.Zero(t, counting.opens,
		"with no legacy cache/bundles there is nothing to scan, so repeated loader builds must cost stats and no directory enumeration at all")
}

// TestGetBundleDirs_ScansOnlyWhenTheLegacyCacheIsPresent is the other half: the
// scan is not dead, it is guarded. Without this the assertion above could pass
// because the signpost stopped working.
func TestGetBundleDirs_ScansOnlyWhenTheLegacyCacheIsPresent(t *testing.T) {
	resetStrictness(t)
	mem := afero.NewMemMapFs()
	appPath := "/app"
	require.NoError(t, mem.MkdirAll(paths.LocalBundlesPath(appPath), 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(paths.CacheBundlesPath(appPath), "authored.yaml"),
		[]byte("name: authored\n"), 0o644))
	counting := &countingFs{Fs: mem}

	cfg := NewFixture(Fixture{AppPaths: []string{appPath}})
	cfg.SetFS(counting)

	mark := strictness.Checkpoint()
	cfg.GetBundleDirs()

	assert.Positive(t, counting.opens, "a legacy cache/bundles tree must actually be scanned")
	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "a stranded authored bundle must be reported, not silently skipped")
	assert.Equal(t, strictness.ClassMigration, findings[0].Class)
	assert.Contains(t, findings[0].Message, "authored.yaml")
}
