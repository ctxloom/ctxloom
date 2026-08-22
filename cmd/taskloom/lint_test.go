package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// addLegacyTags writes text+tags directly through the underlying
// tasks.Store, bypassing operations.AddTaskWithTags' write-seam
// validateTag gate entirely (see internal/shared/tasks/operations's
// validateTag doc). It exists to simulate tag data written BEFORE that gate
// existed, or by a foreign writer this project's schema never saw -- exactly
// the scenario `taskloom lint`'s advisory, read-time sweep is FOR. A value
// used here that violates a declared enum or range (e.g. below) is now
// rejected outright by AddTaskWithTags itself; going through the store
// directly is the only way to get such data written at all once the gate
// exists.
func addLegacyTags(t *testing.T, tc operations.TaskContext, text string, tags ...string) string {
	t.Helper()
	_, logPath, err := operations.ResolveLogPath(tc)
	require.NoError(t, err)
	store, err := tasks.OpenLog(logPath, tc.SessionHarp)
	require.NoError(t, err)
	task, err := store.AddWithTags(text, "", "", tags...)
	require.NoError(t, err)
	return task.HarpID
}

// TestRunLintCmd_FlagsBadKindAndExitsNonZero exercises `taskloom lint`
// against the project's DEFAULT tag_schema: a triage:kind value outside the
// closed enum is reported by harp ID and reason, and the command itself
// returns a non-zero-exit error (useful for CI) despite lint never blocking
// the write that created the bad data in the first place -- here the bad
// data is seeded via addLegacyTags precisely because the write-seam gate
// (this feature) now rejects it at write time; lint's job is the sweep for
// data that predates or evades that gate.
func TestRunLintCmd_FlagsBadKindAndExitsNonZero(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	harpID := addLegacyTags(t, tc, "triage this", "triage:kind=sparkles")

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.Error(t, err, "lint must exit non-zero when a violation is found")
	assert.Contains(t, err.Error(), "1 triage-standard violation")
	assert.Contains(t, out.String(), harpID)
	assert.Contains(t, out.String(), "triage:kind")
}

// TestRunLintCmd_CleanDataPassesWithZeroExit proves lint is quiet and
// succeeds when every task's tags already satisfy the declared standard.
func TestRunLintCmd_CleanDataPassesWithZeroExit(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	_, err = operations.AddTaskWithTags(tc, "triage this", "", "", []string{"triage:kind=defect", "triage:exposed=cli", "triage:effort=3"})
	require.NoError(t, err)

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "no triage-standard violations found")
}

// TestRunLintCmd_FlagsOutOfRangeEffort pins the range-facet check against
// the shipped default (triage:effort declared "0,5"). The out-of-range
// value is seeded via addLegacyTags -- AddTaskWithTags itself now rejects
// "triage:effort=9" at write time (see
// operations.TestAddTaskWithTagsRejectsDeclaredRangeViolation).
func TestRunLintCmd_FlagsOutOfRangeEffort(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	addLegacyTags(t, tc, "triage this", "triage:effort=9")

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.Error(t, err)
	assert.Contains(t, out.String(), "outside the declared range")
}

// TestRunLintCmd_FlagsLevelOutsideTheLadder pins the range facet against the
// shipped default's `triage:level` ("1,5"). The ladder has five rungs and
// nothing either side of them: 0 and 6 are not milder-than-wishlist and
// worse-than-critical, they are typos, and a typo that lands silently makes
// a task rank as if it were never triaged at all. Both boundary misses are
// seeded via addLegacyTags because AddTaskWithTags now refuses them at the
// write seam -- there is no other way to get such data into the log.
func TestRunLintCmd_FlagsLevelOutsideTheLadder(t *testing.T) {
	for _, bad := range []string{"triage:level=0", "triage:level=6"} {
		t.Run(bad, func(t *testing.T) {
			taskstest.ProjectDir(t)
			tc, err := taskContextSingle()
			require.NoError(t, err)

			harpID := addLegacyTags(t, tc, "triage this", bad)

			var out strings.Builder
			err = runLintCmd(&out, tc, clifmt.FormatText)
			require.Error(t, err, "a level outside the declared ladder must exit non-zero")
			assert.Contains(t, out.String(), harpID)
			assert.Contains(t, out.String(), "triage:level")
			assert.Contains(t, out.String(), "outside the declared range")
		})
	}
}

// The negative half of the range check: every rung of the ladder is legal,
// and a range that rejected a real level would be worse than no range at
// all. Written through AddTaskWithTags so this also proves the WRITE SEAM
// accepts 1..5 rather than merely that lint tolerates them.
func TestRunLintCmd_AcceptsEveryRungOfTheLadder(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	for _, level := range []string{"1", "2", "3", "4", "5"} {
		_, err = operations.AddTaskWithTags(tc, "rung "+level, "", "", []string{"triage:level=" + level})
		require.NoError(t, err, "triage:level=%s is a declared rung and must be writable", level)
	}

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "no triage-standard violations found")
}

// triage:level is declared arity=scalar, so the write seam must collapse a
// task down to ONE of them. Two levels on one task is not a task that is
// both critical and minor -- it is a task whose rank depends on which tag
// the formula's last-write-wins fold happens to land on.
func TestAddTaskWithTags_CollapsesTwoLevelsToOne(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	res, err := operations.AddTaskWithTags(tc, "double-rated", "", "", []string{"triage:level=1", "triage:level=4"})
	require.NoError(t, err)

	var levels []string
	for _, tag := range res.Task.Tags {
		if strings.HasPrefix(tag, "triage:level") {
			levels = append(levels, tag)
		}
	}
	assert.Equal(t, []string{"triage:level=4"}, levels,
		"arity=scalar keeps the LAST value written, never both")
}
