package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestRunLintCmd_FlagsBadTypeAndExitsNonZero exercises `taskloom lint`
// against the project's DEFAULT tag_schema: a triage:type value outside the
// closed enum is reported by harp ID and reason, and the command itself
// returns a non-zero-exit error (useful for CI) despite lint never blocking
// the write that created the bad data in the first place.
func TestRunLintCmd_FlagsBadTypeAndExitsNonZero(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	add, err := operations.AddTaskWithTags(tc, "triage this", "", "", []string{"triage:type=sparkles"})
	require.NoError(t, err)

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.Error(t, err, "lint must exit non-zero when a violation is found")
	assert.Contains(t, err.Error(), "1 triage-standard violation")
	assert.Contains(t, out.String(), add.Task.HarpID)
	assert.Contains(t, out.String(), "triage:type")
}

// TestRunLintCmd_CleanDataPassesWithZeroExit proves lint is quiet and
// succeeds when every task's tags already satisfy the declared standard.
func TestRunLintCmd_CleanDataPassesWithZeroExit(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	_, err = operations.AddTaskWithTags(tc, "triage this", "", "", []string{"triage:type=security", "triage:impact=3"})
	require.NoError(t, err)

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "no triage-standard violations found")
}

// TestRunLintCmd_FlagsOutOfRangeImpact pins the range-facet check against
// the shipped default (triage:impact declared "0,5").
func TestRunLintCmd_FlagsOutOfRangeImpact(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	_, err = operations.AddTaskWithTags(tc, "triage this", "", "", []string{"triage:impact=9"})
	require.NoError(t, err)

	var out strings.Builder
	err = runLintCmd(&out, tc, clifmt.FormatText)
	require.Error(t, err)
	assert.Contains(t, out.String(), "outside the declared range")
}
