package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// statCountingFs counts Stat calls per suffix. countingFs (stranded_bundles_test.go)
// already counts Open in this package and is deliberately left alone; the walk
// measured here is driven entirely by Stat, so it needs its own counter rather
// than a second responsibility bolted onto that one.
type statCountingFs struct {
	afero.Fs
	stats map[string]int
}

func (f *statCountingFs) Stat(name string) (os.FileInfo, error) {
	f.stats[filepath.Base(name)]++
	return f.Fs.Stat(name)
}

// TestFindAppDir_WorktreeProbePerAncestor is the MEASUREMENT U092-F06 asserted
// without one. The row claims DetectWorktree runs once per cwd ancestor with no
// memoization anywhere. Both halves are true and are pinned here so the number
// is a fact the suite maintains rather than an estimate in a review doc.
//
// This pin does NOT endorse memoizing the probe. It exists so that if anyone
// ever does, the change is visible here rather than silent — caching a
// filesystem classification across a long-lived `ctxloom mcp`/`acp` process
// means a worktree created or removed mid-session is classified from stale
// state, which is a decision for a human, not a sweep.
//
// The walk runs against a MemMapFs on purpose: findAppDir takes the cwd from
// the real process but stats through the injected fs, so a mem fs measures the
// same traversal while guaranteeing the probe cannot create or read anything on
// real disk (its home fallback ends in MkdirAll).
func TestFindAppDir_WorktreeProbePerAncestor(t *testing.T) {
	testsupport.Isolate(t)
	resetStrictness(t)

	deep := filepath.Join(t.TempDir(), "a", "b", "c", "d")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	testsupport.ChangeDir(t, deep)

	fs := &statCountingFs{Fs: afero.NewMemMapFs(), stats: map[string]int{}}

	// Fixture hostility, from findAppDir's point of view: the walk only reaches
	// the worktree probe for ancestors that have NO .ctxloom of their own, so
	// nothing on this fs may carry one. A mem fs seeded by an earlier test, or
	// a stray marker, would end the walk on its first iteration and leave every
	// assertion below passing vacuously.
	for dir := deep; ; {
		_, err := fs.Fs.Stat(filepath.Join(dir, AppDirName))
		require.Error(t, err, "fixture is not hostile: %s already carries a %s", dir, AppDirName)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	findAppDir(fs)

	ctxloomStats := fs.stats[AppDirName]
	gitStats := fs.stats[".git"]

	t.Logf("one findAppDir from depth %d: %d %s stats, %d .git stats",
		strings.Count(deep, string(os.PathSeparator)), ctxloomStats, AppDirName, gitStats)

	require.GreaterOrEqual(t, ctxloomStats, 4,
		"fixture is not hostile: the walk visited %d ancestors, too few to measure a per-ancestor cost", ctxloomStats)
	assert.Equal(t, ctxloomStats, gitStats,
		"the linked-worktree probe runs once per ancestor the walk passes through: "+
			"every directory without a %s costs a second stat for its .git entry", AppDirName)

	// No memoization anywhere: a second resolution repeats the whole traversal.
	before := gitStats
	findAppDir(fs)
	assert.Equal(t, 2*before, fs.stats[".git"],
		"the probe is not memoized — a second findAppDir re-stats every ancestor's .git")
}

// TestAmbientStampReWalks pins the multiplier that makes the per-ancestor cost
// matter: config.Load's ambient memo does not protect the walk. Load computes
// ambientStamp BEFORE consulting the cache, and ambientStamp calls findAppDir,
// so a cache HIT still pays the full traversal — which is why the row's "on
// every config.Load" is accurate rather than loose.
//
// Observed through the source rather than a counter because ambientStamp builds
// its own afero.NewOsFs() and offers no injection point; that missing seam is
// itself part of what an eventual fix would have to add.
//
// The source path comes from runtime.Caller, never the cwd: several tests in
// this package chdir, and a source-scanning assertion rooted at "." would read
// nothing, match nothing and still exit 0 — a gate that evaporates instead of
// failing.
func TestAmbientStampReWalks(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller could not resolve this test's own source path")

	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "config.go"))
	require.NoError(t, err)
	body := string(src)

	start := strings.Index(body, "func ambientStamp()")
	require.NotEqual(t, -1, start, "ambientStamp is gone or renamed; re-derive this pin")
	end := strings.Index(body[start:], "\n}\n")
	require.NotEqual(t, -1, end)
	stamp := body[start : start+end]

	assert.Contains(t, stamp, "findAppDir(",
		"ambientStamp resolves the app dir on every call, so Load's memo does not "+
			"spare the cwd walk or the per-ancestor worktree probe")
}
