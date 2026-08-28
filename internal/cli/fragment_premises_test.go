package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// renderFor is the exact rendering the command performs, exercised directly.
// The command's own body is config loading plus this branch; the branch is what
// carries a decision, and it is the one this project's characteristic bug lives
// in — exit 0, a confident message, and nothing said about what was actually
// found.
// renderFor calls the command's OWN rendering function, not a copy of it. An
// earlier version of this test reimplemented the branch, which meant it passed
// regardless of what the command actually did.
func renderFor(t *testing.T, entries []operations.PremiseIndexEntry) string {
	t.Helper()
	return renderPremiseListing(entries)
}

// An empty index must SAY it is empty. Printing an instruction to select from
// nothing would ask an agent to choose from an empty set, and printing nothing
// at all is indistinguishable from the command failing to run.
func TestFragmentPremises_EmptyIndexSaysSoRatherThanRenderingAnEmptyMenu(t *testing.T) {
	got := renderFor(t, nil)

	assert.Contains(t, got, "No fragments carry a premise",
		"an empty corpus must be reported, not rendered as an empty menu")
	assert.Contains(t, got, "loaded unconditionally",
		"and it must say WHY there is nothing to choose — absence of a premise is an assertion")
	assert.NotContains(t, got, "NOT in your context",
		"the selection instruction must not appear when there is nothing to select")
}

// A populated index must carry the qualified refs a selection quotes back, and
// the instruction that decides how well selection performs.
func TestFragmentPremises_RendersQualifiedRefsAndTheInstruction(t *testing.T) {
	got := renderFor(t, []operations.PremiseIndexEntry{
		{Name: "sel#fragments/gamma", Premise: "You are about to remove a worktree."},
	})

	require.NotEmpty(t, got)
	assert.Contains(t, got, "sel#fragments/gamma",
		"the ref is what makes the follow-up ask exact; a bare name can resolve to another bundle's fragment")
	assert.Contains(t, got, "You are about to remove a worktree.",
		"the premise is the thing being decided on and must render verbatim")
	assert.Contains(t, got, "ON ITS OWN",
		"per-premise judgement is the largest measured effect on selection quality")
	assert.NotContains(t, got, "No fragments carry a premise",
		"a populated index must not also claim emptiness")
}
