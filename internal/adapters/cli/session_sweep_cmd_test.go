package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// ssSeedRecorded is crSeedSession with its record: a purge acts only on a
// session the index knows.
func ssSeedRecorded(t *testing.T, harp string, age time.Duration) string {
	t.Helper()
	dir := crSeedSession(t, harp, age)
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionSidecarFileName), []byte("project_dir: /elsewhere\n"), 0o644))
	cotBackdate(t, dir, age)
	return dir
}

// ssPurgeRow is the report's purge row, or the zero row.
func ssPurgeRow(rep operations.SweepReport) operations.SweepRow {
	for _, r := range rep.Rows {
		if r.Action == operations.SweepPurge {
			return r
		}
	}
	return operations.SweepRow{}
}

func resetSessionSweepFlags() {
	sessionSweepOlderThan, sessionSweepPurgeOlderThan = "", ""
	sessionSweepAllProjects, sessionSweepYes = false, false
}

func ssRun(t *testing.T, args ...string) operations.SweepReport {
	t.Helper()
	t.Cleanup(resetSessionSweepFlags)
	out, err := execRootCmd(t, append([]string{"session", "sweep"}, args...)...)
	require.NoError(t, err)
	var rep operations.SweepReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	return rep
}

// Purging has no default: with neither the flag nor session_purge_age the
// purge row is held and --yes leaves the session's bytes.
func TestSessionSweep_PurgeHasNoDefault(t *testing.T) {
	cotProject(t)
	dir := ssSeedRecorded(t, "aged-quiet-heron", 90*24*time.Hour)

	rep := ssRun(t, "--all-projects", "--yes", "--format", "json")

	assert.Equal(t, operations.SweepHeld, ssPurgeRow(rep).Verdict)
	assert.True(t, rep.PurgeCutoff.IsZero())
	cotAssertPlan(t, dir, true)
}

// session_purge_age supplies the purge bound, and the flag beats it.
func TestSessionSweep_PurgeAgeFromConfigAndFlag(t *testing.T) {
	cotProject(t)
	cotHomeConfig(t, "version: 6\nsession_purge_age: 60d\n")
	ssSeedRecorded(t, "aged-quiet-heron", 90*24*time.Hour)

	rep := ssRun(t, "--all-projects", "--format", "json")
	assert.Equal(t, operations.SweepPlanned, ssPurgeRow(rep).Verdict, "session_purge_age 60d plans the purge of a 90-day-old session")

	rep = ssRun(t, "--all-projects", "--purge-older-than", "120d", "--format", "json")
	assert.Empty(t, ssPurgeRow(rep).Action, "a 90-day-old session is younger than a 120-day bound")
}

// A purge age the grammar cannot parse is refused, naming the key.
func TestSessionSweep_RefusesAMalformedPurgeAge(t *testing.T) {
	cotProject(t)
	cotHomeConfig(t, "version: 6\nsession_purge_age: later\n")
	t.Cleanup(resetSessionSweepFlags)
	_, err := execRootCmd(t, "session", "sweep", "--format", "json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session_purge_age")
}
