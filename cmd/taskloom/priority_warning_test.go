package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
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
