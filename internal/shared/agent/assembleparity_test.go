package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two assemblers both produce "the assembled context", contextfile.go documents
// that they must not diverge, and they DO. This file is the evidence.
//
//	AssembleContext        (base.go)        — plain join. No dedup, no warning.
//	                        Production callers: lm/grpc/server.go's SkipSetup
//	                        fan-out path and lm/backends/mock.go.
//	assembleDedupedContext (contextfile.go) — dedups by content hash and emits
//	                        the oversize warning. The assembly half of
//	                        WriteContextFile, i.e. the full-setup path.
//
// contextfile.go states the invariant itself: assembleDedupedContext "differs
// from the simpler exported AssembleContext (no dedup, no warning) WHOSE OUTPUT
// THE RAW CONTEXT FILE MUST NOT DIVERGE FROM."
//
// The first test below CHARACTERIZES the divergence as it stands today, so the
// exact behaviour is pinned and green. The second states the documented
// invariant and is deliberately t.Skip'd: collapsing the two CHANGES the bytes
// the SkipSetup path delivers into a session, which is the human's call, not an
// implementer's. Un-skip it with the fix.

// dupFragments is a fragment set that reaches identical content twice — the
// "same fragment through two bundles" case the dedup exists for.
func dupFragments() []*Fragment {
	return []*Fragment{
		{Name: "bundle-a/standards", Content: "always write the test first"},
		{Name: "bundle-b/standards", Content: "always write the test first"},
		{Name: "bundle-a/style", Content: "prefer small functions"},
	}
}

// TestAssembleContext_DivergesFromTheDelivered is GREEN: it records exactly how
// the two assemblers disagree today, so the escalation rests on a measurement
// and any future change to either one has to face this file.
func TestAssembleContext_DivergesFromTheDelivered(t *testing.T) {
	t.Run("dedup: same content through two bundles", func(t *testing.T) {
		plain := AssembleContext(dupFragments())
		deduped := assembleDedupedContext(dupFragments())

		assert.Equal(t, 2, strings.Count(plain, "always write the test first"),
			"AssembleContext emits the duplicated content TWICE")
		assert.Equal(t, 1, strings.Count(deduped, "always write the test first"),
			"assembleDedupedContext emits it once")
		assert.NotEqual(t, plain, deduped,
			"THE DEFECT: one fragment set, two different assembled contexts")
	})

	t.Run("oversize warning: only one path warns", func(t *testing.T) {
		big := []*Fragment{{Name: "huge", Content: strings.Repeat("x", MaxRecommendedContextSize+1)}}

		var dedupedErr bytes.Buffer
		deduped := assembleDedupedContext(big, WithContextStderr(&dedupedErr))
		require.NotEmpty(t, deduped)
		assert.Contains(t, dedupedErr.String(), "recommended max",
			"the full-setup path warns on an oversize context")

		// AssembleContext has no stderr sink at all — it cannot warn, so the
		// SkipSetup path delivers an oversize context in silence.
		assert.NotEmpty(t, AssembleContext(big))
	})

	t.Run("agreement when there is nothing to dedup", func(t *testing.T) {
		same := []*Fragment{
			{Name: "a", Content: "first"},
			{Name: "b", Content: "second"},
		}
		assert.Equal(t, assembleDedupedContext(same), AssembleContext(same),
			"with no duplicate content the two do agree — the divergence is dedup, not framing")
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
// contextfile.go's own doc asserts. It FAILS today. It is skipped, not deleted,
// because making it pass changes what the SkipSetup fan-out path
// (lm/grpc/server.go:189) delivers into a live session — an escalation, not a
// cleanup. Delete the Skip when the two are collapsed.
func TestAssembleContext_MustNotDivergeFromTheDelivered(t *testing.T) {
	t.Skip("U100-F13 ESCALATED: production is unfixed. Collapsing the two assemblers changes the bytes the SkipSetup path delivers; that decision is the human's, not this test's.")

	assert.Equal(t, assembleDedupedContext(dupFragments()), AssembleContext(dupFragments()),
		"contextfile.go documents that the raw context file must NOT diverge from AssembleContext's output")
}
