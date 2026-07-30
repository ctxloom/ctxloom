package priority

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

// The set of statuses this package treats as terminal must be exactly the
// set the store publishes as terminal. tasks.Statuses() is the taxonomy's
// single source of truth (its own doc: "the single source of truth a client
// can read instead of baking in ... which statuses are terminal"), and
// rank-normalization's population is where this package's answer becomes
// observable: a terminal task is left out and keeps Priority 0, a
// non-terminal one is ranked.
//
// This is the parity gate over what used to be two hand-maintained copies of
// one predicate. It is driven from tasks.Statuses(), not from a literal list,
// so ADDING a status to the taxonomy extends the check automatically rather
// than leaving a new status silently classified by only one of the two.
func TestTerminalStatusesMatchTheStoreTaxonomy(t *testing.T) {
	statuses := tasks.Statuses()
	require.NotEmpty(t, statuses)

	// One task per status, all otherwise identical, so nothing but the
	// status can decide whether it is ranked.
	all := make([]tasks.Task, 0, len(statuses))
	for _, s := range statuses {
		all = append(all, tasks.Task{HarpID: s.Name, Status: s.Name, CreatedAt: fixedNow})
	}
	results, diag, err := Compute(all, nil, fixedNow)
	require.NoError(t, err)

	wantNonTerminal := 0
	for _, s := range statuses {
		if s.Terminal {
			assert.Zero(t, results[s.Name].Priority,
				"status %q is terminal in tasks.Statuses() but was ranked here", s.Name)
			assert.False(t, isTerminal(s.Name) != s.Terminal,
				"isTerminal(%q) disagrees with the store taxonomy", s.Name)
			continue
		}
		wantNonTerminal++
		assert.NotZero(t, results[s.Name].Priority,
			"status %q is non-terminal in tasks.Statuses() but was excluded from the ranking", s.Name)
		assert.True(t, !isTerminal(s.Name),
			"isTerminal(%q) disagrees with the store taxonomy", s.Name)
	}
	assert.Equal(t, wantNonTerminal, diag.NonTerminalTasks)
}
