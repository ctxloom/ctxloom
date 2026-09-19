package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One concept, ONE assembler. This file used to pin a measured divergence:
//
//	AssembleContext        (base.go)        — plain join. No dedup, no warning.
//	assembleDedupedContext (contextfile.go) — drops a second copy of a fragment
//	                        already assembled, keyed on (Name, content), and
//	                        emits the oversize warning.
//
// contextfile.go stated the invariant against itself — the raw context file
// "must NOT diverge from" AssembleContext's output — and the two diverged, so
// the bytes a run delivered depended on which route it took, and only one route
// warned about an oversize context. AssembleContext is now assembleDedupedContext
// under its exported name, which is what makes the invariant hold by
// construction instead of by inspection.
//
// These tests stay because the property is worth holding onto, not because the
// divergence does: they pin what the one assembler does with each interesting
// fragment set, and the last one states the invariant directly.

// dupFragments is a fragment set in which two DIFFERENT items — different
// bundles, different names — carry identical content. Both must survive: they
// are two authored fragments, and dropping either delivers one publisher's
// content in place of another's.
func dupFragments() []*Fragment {
	return []*Fragment{
		{Name: "bundle-a/standards", Content: "always write the test first"},
		{Name: "bundle-b/standards", Content: "always write the test first"},
		{Name: "bundle-a/style", Content: "prefer small functions"},
	}
}

// reIngestedFragment is ONE item that reached the fragment list twice — the
// same name, the same bytes. This is what dedup is actually for.
func reIngestedFragment() []*Fragment {
	return []*Fragment{
		{Name: "bundle-a/standards", Content: "always write the test first"},
		{Name: "bundle-a/standards", Content: "always write the test first"},
		{Name: "bundle-a/style", Content: "prefer small functions"},
	}
}

// TestAssembleContext_OneAssembler pins what the single assembler does with
// each fragment set that used to tell the two apart.
func TestAssembleContext_OneAssembler(t *testing.T) {
	t.Run("two different fragments with identical content: BOTH survive", func(t *testing.T) {
		plain := AssembleContext(dupFragments())

		assert.Equal(t, 2, strings.Count(plain, "always write the test first"),
			"two different items that merely say the same thing must NOT collapse — that is data loss, not dedup")
		assert.Equal(t, assembleDedupedContext(dupFragments()), plain)
	})

	t.Run("dedup: one item re-ingested collapses on EVERY path", func(t *testing.T) {
		plain := AssembleContext(reIngestedFragment())

		assert.Equal(t, 1, strings.Count(plain, "always write the test first"),
			"the same item reaching the list twice is emitted once")
		assert.Equal(t, assembleDedupedContext(reIngestedFragment()), plain,
			"there is no second route that skips the dedup")
	})

	t.Run("oversize warning: EVERY path warns", func(t *testing.T) {
		big := []*Fragment{{Name: "huge", Content: strings.Repeat("x", MaxRecommendedContextSize+1)}}

		var warned bytes.Buffer
		out := AssembleContext(big, WithContextStderr(&warned))
		require.NotEmpty(t, out)
		assert.Contains(t, warned.String(), "recommended max",
			"an oversize context must not be deliverable in silence by choosing a route")
	})

	t.Run("empty input", func(t *testing.T) {
		assert.Equal(t, "", AssembleContext(nil))
		assert.Equal(t, "", assembleDedupedContext(nil))
		blank := []*Fragment{{Name: "a", Content: ""}}
		assert.Equal(t, "", AssembleContext(blank))
		assert.Equal(t, "", assembleDedupedContext(blank))
	})
}

// TestAssembleContext_MustNotDivergeFromTheDelivered states the invariant
// contextfile.go's doc asserts, and was t.Skip'd as U100-F13 while production
// carried two assemblers. It is un-skipped: there is one assembler, so the
// invariant is now a fact about the code rather than a hope about it.
func TestAssembleContext_MustNotDivergeFromTheDelivered(t *testing.T) {
	assert.Equal(t, assembleDedupedContext(reIngestedFragment()), AssembleContext(reIngestedFragment()),
		"the raw context file must NOT diverge from AssembleContext's output")
}
