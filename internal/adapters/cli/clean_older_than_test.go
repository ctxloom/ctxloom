package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// EVERY OUTCOME HERE IS ASSERTED ON THE FILESYSTEM: which files exist after,
// and with which bytes. The JSON payload is read only for the shape of the
// report, never as proof that anything happened.

// resetCleanFlags restores `clean`'s package-level cobra flag vars, for the
// same reason resetSessionWorktreesFlags exists: pflag only calls Set() on
// flags present in a given argv, so a value one test left on survives into a
// later invocation that never mentions the flag.
func resetCleanFlags() {
	cleanYes = false
	cleanOlderThan = ""
	cleanIncludePersist = false
}

// cotProject isolates HOME, roots the project at a fresh temp dir, and
// invalidates the ambient config so `clean` resolves both its cache and its
// config there rather than in this repository.
func cotProject(t *testing.T) string {
	t.Helper()
	t.Cleanup(resetCleanFlags)
	testsupport.Isolate(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.AppDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.AppDirName, paths.ConfigFileName+".yaml"),
		[]byte("version: 6\n"), 0o644))
	t.Setenv(projectroot.EnvVar, dir)
	resetApp()
	t.Cleanup(resetApp)
	return dir
}

// cotHomeConfig writes ~/.ctxloom/config.yaml — the layer session_reap_age
// is honoured from.
func cotHomeConfig(t *testing.T, body string) {
	t.Helper()
	home, err := paths.HomeConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, paths.ConfigFileName+".yaml"), []byte(body), 0o644))
	resetApp()
}

const (
	cotScratch = "disposable scratch\n"
	cotPlan    = "# a plan someone cites\n"
)

// cotSeedSession plants a harp directory in the session layout — an
// ephemeral/ file and a persist/ plan — whose owner is provably gone, aged
// by the given amount.
func cotSeedSession(t *testing.T, harp string, age time.Duration) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.EphemeralDirName), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.PersistDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.EphemeralDirName, "scratch.txt"), []byte(cotScratch), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.PersistDirName, "design"+paths.PlanFileExt), []byte(cotPlan), 0o644))

	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
	cotBackdate(t, dir, age)
	return dir
}

// cotBackdate sets every mtime under the harp directory, and its lock file,
// to age ago — after any step that stamps a fresh one.
func cotBackdate(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	require.NoError(t, filepath.Walk(dir, func(p string, _ os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		return os.Chtimes(p, old, old)
	}))
	lock, err := paths.HarpLockPath(filepath.Base(dir))
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(lock, old, old))
}

func cotAssertScratch(t *testing.T, dir string, present bool) {
	t.Helper()
	p := filepath.Join(dir, paths.EphemeralDirName, "scratch.txt")
	if !present {
		assert.NoFileExists(t, p, "ephemeral/ must be reclaimed")
		return
	}
	got, err := os.ReadFile(p)
	require.NoError(t, err, "ephemeral/ must survive")
	assert.Equal(t, cotScratch, string(got))
}

func cotAssertPlan(t *testing.T, dir string, present bool) {
	t.Helper()
	p := filepath.Join(dir, paths.PersistDirName, "design"+paths.PlanFileExt)
	if !present {
		assert.NoFileExists(t, p, "persist/ must be reclaimed")
		return
	}
	got, err := os.ReadFile(p)
	require.NoError(t, err, "persist/ must survive")
	assert.Equal(t, cotPlan, string(got))
}

func cotRun(t *testing.T, args ...string) cleanReport {
	t.Helper()
	out, err := execRootCmd(t, append([]string{"clean"}, args...)...)
	require.NoError(t, err)
	var rep cleanReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	return rep
}

// TestClean_WithoutOlderThan_ReapsOnTheDefaultAge: a plain `ctxloom clean
// --yes` sweeps ephemeral/ on the built-in default age — a session older
// than it loses its scratch, one younger keeps it — and persist/ is never
// part of that.
func TestClean_WithoutOlderThan_ReapsOnTheDefaultAge(t *testing.T) {
	cotProject(t)
	aged := cotSeedSession(t, "aged-quiet-heron", 90*24*time.Hour)
	young := cotSeedSession(t, "young-quiet-heron", 10*24*time.Hour)

	rep := cotRun(t, "--yes", "--format", "json")

	assert.Equal(t, 1, rep.Sessions.Reclaimed)
	assert.Equal(t, 1, rep.Sessions.Newer)
	cotAssertScratch(t, aged, false)
	cotAssertPlan(t, aged, true)
	cotAssertScratch(t, young, true)
	cotAssertPlan(t, young, true)
}

// TestClean_HonoursSessionReapAgeFromHomeConfig: the age is configurable,
// from the home file — a ten-day-old session that the default would keep
// is reaped when session_reap_age says five days.
func TestClean_HonoursSessionReapAgeFromHomeConfig(t *testing.T) {
	cotProject(t)
	cotHomeConfig(t, "version: 6\nsession_reap_age: 5d\n")
	dir := cotSeedSession(t, "aged-quiet-heron", 10*24*time.Hour)

	rep := cotRun(t, "--yes", "--format", "json")

	assert.Equal(t, 1, rep.Sessions.Reclaimed)
	cotAssertScratch(t, dir, false)
	cotAssertPlan(t, dir, true)
}

// TestClean_RefusesAMalformedSessionReapAge: a configured age the grammar
// cannot parse is refused, naming the key — never resolved to the default,
// which would reap on an age nobody chose. Nothing on disk changes.
func TestClean_RefusesAMalformedSessionReapAge(t *testing.T) {
	cotProject(t)
	cotHomeConfig(t, "version: 6\nsession_reap_age: soon\n")
	dir := cotSeedSession(t, "aged-quiet-heron", 90*24*time.Hour)

	_, err := execRootCmd(t, "clean", "--yes", "--format", "json")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "session_reap_age", "the refusal names the thing to fix")
	cotAssertScratch(t, dir, true)
	cotAssertPlan(t, dir, true)
}

// TestClean_OlderThan_OverridesTheConfiguredAge: the flag is one
// invocation's bound and beats the configured one in both directions.
func TestClean_OlderThan_OverridesTheConfiguredAge(t *testing.T) {
	cotProject(t)
	cotHomeConfig(t, "version: 6\nsession_reap_age: 5d\n")
	dir := cotSeedSession(t, "aged-quiet-heron", 10*24*time.Hour)

	cotRun(t, "--older-than", "20d", "--yes", "--format", "json")
	cotAssertScratch(t, dir, true)

	cotRun(t, "--older-than", "7d", "--yes", "--format", "json")
	cotAssertScratch(t, dir, false)
}

// TestClean_OlderThan_ReportsWithoutYes pins the read-only default: without
// --yes the plan is produced and NOT ONE BYTE moves, on a TTY or not.
func TestClean_OlderThan_ReportsWithoutYes(t *testing.T) {
	cotProject(t)
	dir := cotSeedSession(t, "aged-quiet-heron", 90*24*time.Hour)

	rep := cotRun(t, "--older-than", "30d", "--include-persist", "--format", "json")

	assert.False(t, rep.Sessions.Applied)
	assert.Equal(t, 0, rep.Sessions.Reclaimed)
	require.Len(t, rep.Sessions.Candidates, 1)
	assert.Equal(t, "aged-quiet-heron", rep.Sessions.Candidates[0].Harp)
	cotAssertScratch(t, dir, true)
	cotAssertPlan(t, dir, true)
}

// TestClean_IncludePersist_ReclaimsPersistToo is the opt-in doing its job,
// and the default arm beside it so the flag is proven to be the difference.
func TestClean_IncludePersist_ReclaimsPersistToo(t *testing.T) {
	cotProject(t)
	dir := cotSeedSession(t, "aged-quiet-heron", 90*24*time.Hour)

	cotRun(t, "--yes", "--format", "json")
	cotAssertScratch(t, dir, false)
	cotAssertPlan(t, dir, true)

	rep := cotRun(t, "--include-persist", "--yes", "--format", "json")

	assert.Equal(t, 1, rep.Sessions.Reclaimed)
	cotAssertPlan(t, dir, false)
	assert.DirExists(t, dir, "the session directory itself stays")
}

// TestClean_KeepMarker_ExemptsTheSession: an empty file named `keep` at the
// top of the session directory takes it out of the sweep, even under
// --include-persist.
func TestClean_KeepMarker_ExemptsTheSession(t *testing.T) {
	cotProject(t)
	dir := cotSeedSession(t, "kept-quiet-heron", 90*24*time.Hour)
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionKeepMarkerFileName), nil, 0o644))
	cotBackdate(t, dir, 90*24*time.Hour)

	rep := cotRun(t, "--include-persist", "--yes", "--format", "json")

	assert.Equal(t, 0, rep.Sessions.Reclaimed)
	assert.Equal(t, 1, rep.Sessions.Kept)
	cotAssertScratch(t, dir, true)
	cotAssertPlan(t, dir, true)
}

// TestClean_LeavesARunningSession pins the refusal end to end: the lock
// decides, and a held lock means hands off.
func TestClean_LeavesARunningSession(t *testing.T) {
	cotProject(t)
	dir := cotSeedSession(t, "live-quiet-heron", 90*24*time.Hour)
	require.NoError(t, sessionlock.Hold("live-quiet-heron"))
	t.Cleanup(func() { sessionlock.Release("live-quiet-heron") })
	cotBackdate(t, dir, 90*24*time.Hour)

	rep := cotRun(t, "--include-persist", "--yes", "--format", "json")

	assert.Equal(t, 0, rep.Sessions.Reclaimed)
	assert.Equal(t, 1, rep.Sessions.Skipped)
	cotAssertScratch(t, dir, true)
	cotAssertPlan(t, dir, true)
}

func TestParseAgeBound(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	t.Run("day offset", func(t *testing.T) {
		got, err := parseAgeBound("--older-than", "30d", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-30*24*time.Hour), got)
	})
	t.Run("week offset", func(t *testing.T) {
		got, err := parseAgeBound("--older-than", "2w", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-14*24*time.Hour), got)
	})
	t.Run("hour offset", func(t *testing.T) {
		got, err := parseAgeBound("--older-than", "720h", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-720*time.Hour), got)
	})
	t.Run("calendar date", func(t *testing.T) {
		got, err := parseAgeBound("--older-than", "2026-01-01", now)
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), got)
	})
	t.Run("the built-in default parses", func(t *testing.T) {
		got, err := parseAgeBound("session_reap_age", config.DefaultSessionReapAge, now)
		require.NoError(t, err, "a default the grammar rejects would refuse every plain clean")
		assert.True(t, got.Before(now))
	})

	// Every rejection must produce a ZERO time WITH an error naming its
	// source. A parse that silently yielded zero-and-nil would arrive at the
	// operation as "no bound stated" — refused there, but for the wrong
	// reason and with the wrong message.
	for _, bad := range []string{"", "   ", "yesterday", "30x", "-5d", "0d", "01-01-2026"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			got, err := parseAgeBound("session_reap_age", bad, now)
			require.Error(t, err, "a bound that cannot be understood must fail loudly")
			assert.Contains(t, err.Error(), "session_reap_age")
			assert.True(t, got.IsZero())
		})
	}
}
