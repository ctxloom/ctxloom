package priority

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

// With no priority_fn declared every task's Raw is 0, and rankNormalize's
// "count of scores <= mine" definition puts a whole tied population at the
// 100th percentile — so every non-terminal task comes back at exactly Max.
// That IS the shipped behaviour and it is deliberate (see Compute's doc):
// an absent formula is a valid, inert configuration, not a fault, so
// Compute must not refuse to answer.
//
// What makes it safe rather than a silent no-op is that Diagnostics says so.
// Both flags must fire, because the CLI/MCP warning is what turns a
// meaningless ranking into a reported one; a change that keeps the Max
// ranking but stops setting either flag re-opens the silent-no-op.
func TestCompute_NoPriorityFn_EveryTaskMaxButDiagnosticsSayItIsMeaningless(t *testing.T) {
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusInProgress, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
		{HarpID: "c", Status: tasks.StatusToDo, Tags: []string{"urgent"}, CreatedAt: fixedNow},
		{HarpID: "done", Status: tasks.StatusDone, CreatedAt: fixedNow},
	}
	results, diag, err := Compute(all, nil, fixedNow)
	require.NoError(t, err, "a nil schema is inert, never an error")

	for _, id := range []string{"a", "b", "c"} {
		assert.Equal(t, Max, results[id].Priority, "task %s", id)
		assert.Zero(t, results[id].Raw, "task %s", id)
	}
	assert.Zero(t, results["done"].Priority, "a terminal task stays out of the population")

	assert.True(t, diag.NoPriorityFn, "the degenerate ranking must be reported, not just produced")
	assert.True(t, diag.AllTied, "every non-terminal task shares one raw score")
}

// ScoredTasks is a numerator. Its own doc rule — "a low ScoredTasks against
// a large non-terminal population" — cannot be evaluated without the
// population size, and a caller cannot recompute that size without
// re-implementing this package's terminal-status predicate. So Compute must
// report the denominator alongside it.
func TestCompute_Diagnostics_ReportsScoredTasksDenominator(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "scored", Status: tasks.StatusToDo, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
		{HarpID: "unrelated", Status: tasks.StatusToDo, Tags: []string{"urgent"}, CreatedAt: fixedNow},
		{HarpID: "bare", Status: tasks.StatusInProgress, CreatedAt: fixedNow},
		{HarpID: "parked", Status: tasks.StatusDeferred, CreatedAt: fixedNow},
		{HarpID: "finished", Status: tasks.StatusDone, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
		{HarpID: "shelved", Status: tasks.StatusArchived, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 1, diag.ScoredTasks)
	assert.Equal(t, 4, diag.NonTerminalTasks,
		"Deferred is non-terminal (it only skips decay); Done and Archived are not")
}

// The denominator must track the same population rank-normalization uses,
// not the whole task list — otherwise the ratio the warning reports is drawn
// from a set no ranking was ever computed over.
func TestCompute_Diagnostics_DenominatorMatchesTheNormalizedPopulation(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, Tags: []string{"triage:kind=chore"}, CreatedAt: fixedNow},
		{HarpID: "done", Status: tasks.StatusDone, CreatedAt: fixedNow},
	}
	results, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	ranked := 0
	for id, r := range results {
		if r.Priority > 0 {
			ranked++
			assert.NotEqual(t, "done", id)
		}
	}
	assert.Equal(t, ranked, diag.NonTerminalTasks)
}
