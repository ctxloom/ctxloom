package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestState_ProjectionIsLossyButSourceAlwaysTravelsWithIt pins the property
// that makes EffectiveTrustResult.State()'s deliberate 7-source -> 3-state
// collapse safe to report.
//
// The collapse is real and intended: retraction renders as `rejected` because
// both withhold permanently and await nothing, and every first-party exemption
// renders as `accepted` because both expose the item. EffectiveTrust's own doc
// states the decision ("Red line: NO STATE WAS ADDED ... Retraction is likewise
// not a fourth STATE"), and adding a fourth would change a persisted review
// vocabulary rather than a rendering.
//
// What keeps it from being a MISREPORT is that State is never the whole
// answer: Source rides alongside it on the same struct, is serialized on the
// same JSON object, and is what both callers stamp next to State
// (internal/cli's itemRow.trust_source, the MCP listing's row). This pins
// that structural guarantee — every state that more than one source collapses
// into is separated again by Source, and Reason() puts the distinction into
// words for the withheld cases.
func TestState_ProjectionIsLossyButSourceAlwaysTravelsWithIt(t *testing.T) {
	cases := []struct {
		decision trust.Decision
		source   trust.Source
		state    trust.State
	}{
		{trust.Deny, trust.SourceRejected, trust.StateRejected},
		{trust.Deny, trust.SourceRetracted, trust.StateRejected},
		{trust.Deny, trust.SourcePending, trust.StatePending},
		{trust.Allow, trust.SourceLocal, trust.StateAccepted},
		{trust.Allow, trust.SourceBuiltin, trust.StateAccepted},
		{trust.Allow, trust.SourceTrustedSigner, trust.StateAccepted},
		{trust.Allow, trust.SourceAccepted, trust.StateAccepted},
	}

	bySource := map[trust.Source]trust.State{}
	for _, tc := range cases {
		res := EffectiveTrustResult{Decision: tc.decision, Source: tc.source}
		assert.Equalf(t, tc.state, res.State(), "source %q", tc.source)
		bySource[tc.source] = res.State()
	}
	require.Len(t, bySource, len(cases), "every declared source must be covered exactly once")

	// The collapse really is many-to-one — otherwise the guarantee below is
	// vacuous.
	byState := map[trust.State]int{}
	for _, s := range bySource {
		byState[s]++
	}
	assert.Greater(t, byState[trust.StateAccepted], 1, "several sources must collapse into accepted")
	assert.Greater(t, byState[trust.StateRejected], 1, "rejection and retraction must collapse into rejected")

	// Source is serialized on the same object as the decision, so a consumer
	// reading `state` from a listing always has `trust_source` beside it.
	raw, err := json.Marshal(EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceRetracted, Detail: "CVE-2026-1"})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	assert.Equal(t, string(trust.SourceRetracted), payload["source"],
		"the source that State() collapses must stay on the wire")

	// And for the two withheld sources that share StateRejected, the words a
	// user is shown differ.
	rejected := EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceRejected}
	retracted := EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceRetracted, Detail: "CVE-2026-1"}
	require.Equal(t, rejected.State(), retracted.State())
	assert.NotEqual(t, rejected.Reason(), retracted.Reason(),
		"a retraction and a human rejection share a state — they must not share an explanation")
	assert.Contains(t, retracted.Reason(), "publisher")
}
