package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetCleanFlags restores `clean`'s package-level cobra flag vars, for the
// same reason resetSessionWorktreesFlags exists: pflag only calls Set() on
// flags present in a given argv, so a value one test left on survives into a
// later invocation that never mentions the flag.
func resetCleanFlags() {
	cleanYes = false
	cleanOlderThan = ""
}

// cotSeedAgedSession plants an aged harp directory whose owner is provably
// gone — the population `--older-than` exists to reclaim.
func cotSeedAgedSession(t *testing.T, harp string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte("{\"bulk\":true}\n"), 0o644))

	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)

	old := time.Now().Add(-90 * 24 * time.Hour)
	require.NoError(t, filepath.Walk(dir, func(p string, _ os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		return os.Chtimes(p, old, old)
	}))
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(lock, old, old))
	return dir
}

// TestClean_WithoutOlderThan_ConsidersNoSession is the safety pin on the CLI
// surface: the age bound has NO default, so a plain `ctxloom clean` must not
// consider — let alone remove — a single session, however old it is. The JSON
// payload must not even carry a sessions object, because "considered, found
// nothing" and "never asked" are different answers.
func TestClean_WithoutOlderThan_ConsidersNoSession(t *testing.T) {
	t.Cleanup(resetCleanFlags)
	testsupport.Isolate(t)
	dir := cotSeedAgedSession(t, "aged-quiet-heron")

	out, err := execRootCmd(t, "clean", "--yes", "--format", "json")
	require.NoError(t, err)

	var rep map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.NotContains(t, rep, "sessions",
		"with no --older-than, clean must not report a session plan at all")
	assert.DirExists(t, dir, "and must not touch a single byte of session data")
}

// TestClean_OlderThan_ReportsWithoutYes pins the read-only default on the new
// flag: stating a bound plans, it does not destroy.
func TestClean_OlderThan_ReportsWithoutYes(t *testing.T) {
	t.Cleanup(resetCleanFlags)
	testsupport.Isolate(t)
	dir := cotSeedAgedSession(t, "aged-quiet-heron")

	out, err := execRootCmd(t, "clean", "--older-than", "30d", "--format", "json")
	require.NoError(t, err)

	var rep cleanReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.NotNil(t, rep.Sessions)
	assert.False(t, rep.Sessions.Applied)
	assert.Equal(t, 0, rep.Sessions.Reclaimed)
	require.Len(t, rep.Sessions.Candidates, 1)
	assert.Equal(t, "aged-quiet-heron", rep.Sessions.Candidates[0].Harp)
	assert.DirExists(t, dir, "a report removes nothing")
}

// TestClean_OlderThanWithYes_ReclaimsAgedSession is the flag doing its job.
func TestClean_OlderThanWithYes_ReclaimsAgedSession(t *testing.T) {
	t.Cleanup(resetCleanFlags)
	testsupport.Isolate(t)
	dir := cotSeedAgedSession(t, "aged-quiet-heron")

	out, err := execRootCmd(t, "clean", "--older-than", "30d", "--yes", "--format", "json")
	require.NoError(t, err)

	var rep cleanReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.NotNil(t, rep.Sessions)
	assert.Equal(t, 1, rep.Sessions.Reclaimed)
	assert.NoDirExists(t, dir)
}

// TestClean_OlderThan_LeavesARunningSession pins the refusal end to end: the
// lock, not the index, decides, and a held lock means hands off.
func TestClean_OlderThan_LeavesARunningSession(t *testing.T) {
	t.Cleanup(resetCleanFlags)
	testsupport.Isolate(t)
	dir := cotSeedAgedSession(t, "live-quiet-heron")
	require.NoError(t, sessionlock.Hold("live-quiet-heron"))
	t.Cleanup(func() { sessionlock.Release("live-quiet-heron") })

	out, err := execRootCmd(t, "clean", "--older-than", "30d", "--yes", "--format", "json")
	require.NoError(t, err)

	var rep cleanReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.NotNil(t, rep.Sessions)
	assert.Equal(t, 0, rep.Sessions.Reclaimed)
	assert.Equal(t, 1, rep.Sessions.Skipped)
	assert.DirExists(t, dir, "a running session's data survives")
}

func TestParseAgeBound(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	t.Run("day offset", func(t *testing.T) {
		got, err := parseAgeBound("30d", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-30*24*time.Hour), got)
	})
	t.Run("week offset", func(t *testing.T) {
		got, err := parseAgeBound("2w", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-14*24*time.Hour), got)
	})
	t.Run("hour offset", func(t *testing.T) {
		got, err := parseAgeBound("720h", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-720*time.Hour), got)
	})
	t.Run("calendar date", func(t *testing.T) {
		got, err := parseAgeBound("2026-01-01", now)
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), got)
	})

	// Every rejection must produce a ZERO time WITH an error. A parse that
	// silently yielded zero-and-nil would arrive at the operation as "no bound
	// stated" — refused there, but for the wrong reason and with the wrong
	// message.
	for _, bad := range []string{"", "   ", "yesterday", "30x", "-5d", "0d", "01-01-2026"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			got, err := parseAgeBound(bad, now)
			require.Error(t, err, "a bound that cannot be understood must fail loudly")
			assert.True(t, got.IsZero())
		})
	}
}
