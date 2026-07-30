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
