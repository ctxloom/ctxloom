package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// A ranking in which every task scores exactly Max — what a project with no
// priority_fn gets — must never reach a user unannounced. This pins the
// consumer half of that contract: the diagnostics priority.Compute returns
// for the degenerate cases render to a NON-EMPTY warning, which
// renderListResult writes to stderr and mcp_tools ships as
// `priority_warning`. If either arm ever returns "" the ranking becomes a
// silent no-op with a plausible-looking payload.
func TestPriorityDiagnosticWarning_DegenerateRankingIsNeverSilent(t *testing.T) {
	cases := []struct {
		name string
		diag priority.Diagnostics
	}{
		{"no priority_fn declared", priority.Diagnostics{NoPriorityFn: true, AllTied: true}},
		{"declared but every task ties", priority.Diagnostics{AllTied: true, ScoredTasks: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEmpty(t, priorityDiagnosticWarning(tc.diag))
		})
	}

	assert.Empty(t, priorityDiagnosticWarning(priority.Diagnostics{ScoredTasks: 3}),
		"a healthy ranking must stay quiet")
}

// The tied-scores warning quotes ScoredTasks. On its own that number cannot
// be read: "only 3 carry a tag any formula reads" is a healthy ranking of 3
// active tasks and a broken one of 300. The warning must carry the
// denominator Compute now reports.
func TestPriorityDiagnosticWarning_TiedWarningNamesBothSides(t *testing.T) {
	msg := priorityDiagnosticWarning(priority.Diagnostics{
		AllTied: true, ScoredTasks: 3, NonTerminalTasks: 300,
	})
	assert.Contains(t, msg, "3 of 300", "the warning must quote the ratio, not a bare numerator")
}

// A formula term carried by NO active task is inert: it resolves to the same
// absent-tag constant for every task in the project and can never change any
// ordering. The ranking around it may look perfectly healthy — untied,
// fully populated, every task scored by SOME term — which is why the
// any-target signal cannot report this and the per-target one must.
func TestPriorityDiagnosticWarning_NamesAnInertFormulaTerm(t *testing.T) {
	msg := priorityDiagnosticWarning(priority.Diagnostics{
		ScoredTasks: 674, NonTerminalTasks: 674,
		TargetCoverage: []priority.TargetCoverage{
			{Target: "triage:data-loss", Tasks: 0},
			{Target: "triage:kind", Tasks: 674},
		},
	})
	assert.Contains(t, msg, "triage:data-loss", "the dead term must be named")
	assert.Contains(t, msg, "inert")
	assert.NotContains(t, msg, "triage:kind", "a fully-covered term is not a finding")
}

// A term applied to a minority of the log ranks the MAJORITY by its absence.
// The number a reader has to act on is that uncovered majority, so the
// warning quotes it against the population rather than quoting the carriers
// — 107 carriers reads as progress, 567 unrated reads as the backlog it is.
func TestPriorityDiagnosticWarning_QuotesTheUNCOVEREDCountForAThinTerm(t *testing.T) {
	msg := priorityDiagnosticWarning(priority.Diagnostics{
		ScoredTasks: 674, NonTerminalTasks: 674,
		TargetCoverage: []priority.TargetCoverage{{Target: "triage:level", Tasks: 107}},
	})
	assert.Contains(t, msg, "567 of 674 active tasks carry no triage:level")
	assert.NotContains(t, msg, "inert", "a thinly-covered term still moves some rankings")
}

// The whole point of a threshold is that it has a quiet side. A formula
// whose every term reaches most of the log is a well-grounded ranking and
// must produce no warning at all, or the warning becomes wallpaper and the
// real finding is skipped with it.
func TestPriorityDiagnosticWarning_WellCoveredFormulaStaysSilent(t *testing.T) {
	assert.Empty(t, priorityDiagnosticWarning(priority.Diagnostics{
		ScoredTasks: 100, NonTerminalTasks: 100,
		TargetCoverage: []priority.TargetCoverage{
			{Target: "triage:effort", Tasks: 50},
			{Target: "triage:level", Tasks: 100},
		},
	}), "a term reaching exactly half the population is grounded enough to stay quiet")
}

// An empty project has no ranking to mislead anyone about, and every term is
// trivially uncovered. Warning here would fire on every fresh project.
func TestPriorityDiagnosticWarning_EmptyPopulationIsNotAFinding(t *testing.T) {
	assert.Empty(t, priorityDiagnosticWarning(priority.Diagnostics{
		NonTerminalTasks: 0,
		TargetCoverage:   []priority.TargetCoverage{{Target: "triage:level", Tasks: 0}},
	}))
}

// With no priority_fn there is no term whose coverage could be discussed and
// only one fix worth naming. The coverage lines must not pile on behind a
// message that already says the ranking reflects nothing.
func TestPriorityDiagnosticWarning_NoPriorityFnReportsAlone(t *testing.T) {
	msg := priorityDiagnosticWarning(priority.Diagnostics{
		NoPriorityFn: true, AllTied: true, NonTerminalTasks: 20,
		TargetCoverage: []priority.TargetCoverage{{Target: "triage:exposed", Tasks: 0}},
	})
	assert.Contains(t, msg, "declares no priority_fn")
	assert.NotContains(t, msg, "triage:exposed")
	assert.NotContains(t, msg, "\n", "the no-formula case is one message, not a list")
}

// The two findings are independent and can both hold: a tied ranking whose
// terms are also uncovered needs to report both, since fixing one does not
// fix the other.
func TestPriorityDiagnosticWarning_TiedAndUncoveredBothReport(t *testing.T) {
	msg := priorityDiagnosticWarning(priority.Diagnostics{
		AllTied: true, ScoredTasks: 0, NonTerminalTasks: 40,
		TargetCoverage: []priority.TargetCoverage{{Target: "triage:level", Tasks: 0}},
	})
	assert.Contains(t, msg, "every active task ties")
	assert.Contains(t, msg, "triage:level")
}

// The coverage warning is multi-line, and clidiag prefixes what it is handed
// ONCE. Emitting it as a single message leaves every line after the first
// bare on stderr, where an unprefixed line is indistinguishable from ordinary
// output -- the channel split this project already has an integration test
// for. Every line must carry the prefix.
func TestRunListCmd_EveryWarningLineIsPrefixedOnStderr(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	// Two tasks that rate each other but leave several formula terms
	// uncarried, so the coverage warning runs to more than one line.
	for _, level := range []string{"1", "5"} {
		_, err = operations.AddTaskWithTags(tc, "rung "+level, "", "", []string{"triage:level=" + level})
		require.NoError(t, err)
	}

	var out, errw strings.Builder
	require.NoError(t, runListCmd(&out, &errw, tc, listOptions{Sort: sortPriority, Format: clifmt.FormatText}))

	var warned int
	for _, line := range strings.Split(strings.TrimRight(errw.String(), "\n"), "\n") {
		if !strings.Contains(line, "active tasks carry no") && !strings.Contains(line, "is inert") {
			continue
		}
		warned++
		assert.Contains(t, line, "taskloom:", "a diagnostic line reached stderr unprefixed: %q", line)
	}
	assert.Greater(t, warned, 1, "this population must produce a multi-line coverage warning, or the test proves nothing")
}
