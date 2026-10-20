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
	"github.com/ctxloom/ctxloom/internal/shared/agebound"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// EVERY OUTCOME HERE IS ASSERTED ON THE FILESYSTEM: which files exist after,
// and with which bytes. The JSON payload is read only for the shape of the
// report, never as proof that anything happened.

// cotProject isolates HOME, roots the project at a fresh temp dir, and
// invalidates the ambient config so `clean` resolves both its cache and its
// config there rather than in this repository.
func cotProject(t *testing.T) string {
	t.Helper()
	t.Cleanup(func() { resetFlags(t, rootCmd) })
	testsupport.Isolate(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.AppDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.AppDirName, paths.ConfigFileName+".yaml"),
		[]byte("schema_version: 8\n"), 0o644))
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
	cotPlan    = "{\"bulk\":true}\n"
)

// cotSeedSession plants a harp directory in the session layout — a scratch/
// file and a transcript under transcripts/ — whose owner is provably gone,
// aged by the given amount. Its last activity is its end, so past
// session_transcript_retention (30d by default) its persistent members are
// --include-persist's to take.
func cotSeedSession(t *testing.T, harp string, age time.Duration) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.ScratchDirName), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.TranscriptsDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.ScratchDirName, "scratch.txt"), []byte(cotScratch), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName), []byte(cotPlan), 0o644))
	out := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionSidecarFileName), []byte("schema_version: 1\nproject_dir: /tmp/demo\noutput_dir: "+out+"\n"), 0o644))

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
	p := filepath.Join(dir, paths.ScratchDirName, "scratch.txt")
	if !present {
		assert.NoFileExists(t, p, "scratch/ must be reclaimed")
		return
	}
	got, err := os.ReadFile(p)
	require.NoError(t, err, "scratch/ must survive")
	assert.Equal(t, cotScratch, string(got))
}

func cotAssertPlan(t *testing.T, dir string, present bool) {
	t.Helper()
	p := filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName)
	if !present {
		assert.NoFileExists(t, p, "transcripts/ must be reclaimed")
		return
	}
	got, err := os.ReadFile(p)
	require.NoError(t, err, "transcripts/ must survive")
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
// --yes` sweeps scratch/ on the built-in default age — a session older
// than it loses its scratch, one younger keeps it — and transcripts/ is never
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
	cotHomeConfig(t, "schema_version: 8\nsession_reap_age: 5d\n")
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
	cotHomeConfig(t, "schema_version: 8\nsession_reap_age: soon\n")
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
	cotHomeConfig(t, "schema_version: 8\nsession_reap_age: 5d\n")
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

// TestClean_IncludePersist_RetainsARecentTranscript: --include-persist takes
// a session's transcript only once session_transcript_retention (30d by
// default) has passed since it ended. A session aged past the reclaim bound
// but within retention gives up its scratch and keeps its transcript.
func TestClean_IncludePersist_RetainsARecentTranscript(t *testing.T) {
	cotProject(t)
	dir := cotSeedSession(t, "recent-quiet-heron", 10*24*time.Hour)

	rep := cotRun(t, "--older-than", "5d", "--include-persist", "--yes", "--format", "json")

	assert.Equal(t, 1, rep.Sessions.Reclaimed, "its ephemeral members are reclaimed")
	cotAssertScratch(t, dir, false)
	cotAssertPlan(t, dir, true)
	require.Len(t, rep.Sessions.Candidates, 1)
	assert.Contains(t, rep.Sessions.Candidates[0].Reason, "retention")
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

// The age grammar itself is agebound's (agebound.TestParse); what is pinned
// here is that ctxloom's own built-in default is a value that grammar accepts.
func TestDefaultSessionReapAge_Parses(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got, err := agebound.Parse("session_reap_age", config.DefaultSessionReapAge, now)
	require.NoError(t, err, "a default the grammar rejects would refuse every plain clean")
	assert.True(t, got.Before(now))
}
