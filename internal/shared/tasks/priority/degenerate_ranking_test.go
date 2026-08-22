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

// ScoredTasks cannot see a formula whose individual terms are dead. It asks
// "did ANY referenced target reach this task", so ONE well-applied term
// carries the whole answer to 100% while every other term in the same
// formula is carried by nothing and contributes an identical constant to
// every score. That is a ranking decided by one term while reporting perfect
// coverage — the precise reading that let a formula of five dead risk flags
// look healthy.
//
// TargetCoverage is the per-term counterpart, and this pins the distinction
// directly: healthy numerator, healthy denominator, and a dead term named.
func TestCompute_Diagnostics_TargetCoverageNamesADeadTermScoredTasksCannotSee(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"widget:score"="{{widget:score}} + {{widget:never}}"`,
	)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"widget:score=1"}, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, Tags: []string{"widget:score=2"}, CreatedAt: fixedNow},
		{HarpID: "c", Status: tasks.StatusInProgress, Tags: []string{"widget:score=3"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	require.Equal(t, diag.NonTerminalTasks, diag.ScoredTasks,
		"every task carries a referenced target, so the any-target signal reads perfectly healthy")
	assert.False(t, diag.AllTied, "and the scores genuinely differ")

	assert.Equal(t, []TargetCoverage{
		{Target: "widget:never", Tasks: 0},
		{Target: "widget:score", Tasks: 3},
	}, diag.TargetCoverage,
		"the dead term must be reported by name, worst coverage first")
}

// A target's coverage count is drawn from the SAME non-terminal population
// rank-normalization runs over. Counting a Done task's tags would let a term
// that only ever appears on finished work report as covered, while it
// contributes an absent-tag constant to every task actually being ranked.
func TestCompute_Diagnostics_TargetCoverageCountsOnlyTheRankedPopulation(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"widget:score"="{{widget:score}} + {{widget:historic}}"`,
	)
	all := []tasks.Task{
		{HarpID: "live", Status: tasks.StatusToDo, Tags: []string{"widget:score=1"}, CreatedAt: fixedNow},
		{HarpID: "finished", Status: tasks.StatusDone, Tags: []string{"widget:score=2", "widget:historic=9"}, CreatedAt: fixedNow},
		{HarpID: "shelved", Status: tasks.StatusArchived, Tags: []string{"widget:historic=9"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, []TargetCoverage{
		{Target: "widget:historic", Tasks: 0},
		{Target: "widget:score", Tasks: 1},
	}, diag.TargetCoverage,
		"a target carried only by terminal tasks is dead for the ranking, and must count as such")
}

// Coverage counts PRESENCE, exactly as a formula's own Tag(...) lookup
// resolves it — so a bare modifier tag and a valued one both count, and a
// value-qualified placeholder reports under its bare target rather than
// under the qualified spelling nothing can ever carry.
func TestCompute_Diagnostics_TargetCoverageCountsPresenceUnderTheBareTarget(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"widget:score"="{{widget:flag=*}} + {{widget:kind=big}}"`,
	)
	all := []tasks.Task{
		{HarpID: "bare", Status: tasks.StatusToDo, Tags: []string{"widget:flag"}, CreatedAt: fixedNow},
		{HarpID: "valued", Status: tasks.StatusToDo, Tags: []string{"widget:flag=yes", "widget:kind=small"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, []TargetCoverage{
		{Target: "widget:kind", Tasks: 1},
		{Target: "widget:flag", Tasks: 2},
	}, diag.TargetCoverage)
}

// A project whose schema declares no formula at all references no target, so
// there is no term whose coverage could be discussed. Reporting an empty
// slice rather than nil would read as "measured, and found nothing", which
// is a different claim.
func TestCompute_Diagnostics_TargetCoverageIsNilWithNoFormula(t *testing.T) {
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}
	_, diag, err := Compute(all, nil, fixedNow)
	require.NoError(t, err)
	assert.Nil(t, diag.TargetCoverage)
}

// The shipped default schema's own coverage rows must be the targets its
// formulas actually read — the list a `--sort priority` warning is rendered
// from. A term silently dropped from (or added to) either formula changes
// this set, and that is exactly the change nobody noticed last time.
func TestCompute_Diagnostics_TargetCoverageCoversEveryDefaultFormulaTerm(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:level=1"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	got := map[string]int{}
	for _, c := range diag.TargetCoverage {
		got[c.Target] = c.Tasks
	}
	assert.Equal(t, map[string]int{
		"triage:level":          1,
		"triage:effort":         0,
		"triage:blocks-release": 0,
		"triage:kind":           0,
		"triage:exposed":        0,
		"triage:blind-gate":     0,
	}, got)
}
