//go:build arch

package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// U023-F01: a future-added ApprovalKind silently converted a TARGETED ladder
// rung into a CATCH-ALL on journal replay — a privilege escalation.
//
// approvalKindName falls back to the wire enum's own String() (e.g.
// "APPROVAL_KIND_SOMETHING_NEW"), which is NOT a key of approvalKindNames
// (whose keys are the short forms "TOOL_USE" etc.). ladderFromFact then does
// `if kind, ok := approvalKindNames[k]; ok` and DROPS it. A rung whose only
// kind was the new one comes back with Kinds == nil — and matches() reads an
// empty Kinds list as "every kind". If that rung's Action is auto_accept, the
// replayed ladder auto-accepts everything.
//
// Two defences, one per test below: the round-trip must fail CLOSED for a
// kind the table does not cover, and the table must be kept covering the whole
// proto enum in the first place.

// TestArch_ApprovalKinds_UnknownKindNeverBecomesCatchAll is the load-bearing
// assertion: a rung journaled with kinds must come back still targeted,
// whatever those kind names were.
func TestArch_ApprovalKinds_UnknownKindNeverBecomesCatchAll(t *testing.T) {
	// The shape a rung takes on the log once a later ctxloom has journaled a
	// kind this build's table does not know.
	facts := []ladderRungFact{{
		Action: string(ActionAutoAccept),
		Kinds:  []string{"APPROVAL_KIND_SOMETHING_NEW"},
	}}

	got := ladderFromFact(facts)
	if !assert.Len(t, got, 1, "the rung itself must survive replay") {
		return
	}

	for name, kind := range approvalKindNames {
		assert.False(t, got[0].matches(kind),
			"a rung whose only journaled kind was unrecognised now auto-accepts %s: "+
				"the unknown name was dropped, leaving Kinds empty, and an empty Kinds list is a catch-all", name)
	}
}

// TestArch_ApprovalKinds_KnownKindsStillMatch keeps the fail-closed defence from
// swallowing the ordinary case: a mix of known and unknown names must still
// answer for the known ones, and only those.
func TestArch_ApprovalKinds_KnownKindsStillMatch(t *testing.T) {
	facts := []ladderRungFact{{
		Action: string(ActionAutoAccept),
		Kinds:  []string{"TOOL_USE", "APPROVAL_KIND_SOMETHING_NEW"},
	}}

	got := ladderFromFact(facts)
	if !assert.Len(t, got, 1) {
		return
	}
	assert.True(t, got[0].matches(agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL_USE),
		"the recognised kind on the rung must still match")
	assert.False(t, got[0].matches(agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE),
		"an unrelated kind must not match a rung that never named it")
}

// TestArch_ApprovalKinds_NameTableCoversTheWholeEnum is the tripwire the fail-closed
// replay behaviour deserves as a companion: the hand-written short-form table
// is deliberately not derived from proto reflection (agent YAML stays readable
// and prefix-free), so nothing but this test notices when the enum grows and
// the table does not. Without it, adding an ApprovalKind quietly produces
// rungs that fail closed forever.
func TestArch_ApprovalKinds_NameTableCoversTheWholeEnum(t *testing.T) {
	for num, wireName := range agentcoordpb.ApprovalRequest_ApprovalKind_name {
		kind := agentcoordpb.ApprovalRequest_ApprovalKind(num)
		if kind == agentcoordpb.ApprovalRequest_APPROVAL_KIND_UNSPECIFIED {
			continue // never journaled as a rung kind
		}
		short := approvalKindName(kind)
		_, known := approvalKindNames[short]
		assert.True(t, known,
			"ApprovalKind %s (%d) has no short form in approvalKindNames: approvalKindName falls back to the "+
				"wire name %q, which ladderFromFact cannot map back — add it to the table",
			wireName, num, short)
	}
}

// TestArch_ApprovalKinds_LadderRoundTripsEveryKind is the property the hand-written table exists
// to satisfy: every enum value survives ladderToFact → ladderFromFact as
// itself, matching only itself.
func TestArch_ApprovalKinds_LadderRoundTripsEveryKind(t *testing.T) {
	for num := range agentcoordpb.ApprovalRequest_ApprovalKind_name {
		kind := agentcoordpb.ApprovalRequest_ApprovalKind(num)
		if kind == agentcoordpb.ApprovalRequest_APPROVAL_KIND_UNSPECIFIED {
			continue
		}
		l := Ladder{{Action: ActionAutoAccept, Kinds: []agentcoordpb.ApprovalRequest_ApprovalKind{kind}}}
		got := ladderFromFact(ladderToFact(l))
		if !assert.Len(t, got, 1) {
			return
		}
		assert.True(t, got[0].matches(kind), "kind %s did not survive the journal round trip", kind)
		for _, other := range approvalKindNames {
			if other == kind {
				continue
			}
			assert.False(t, got[0].matches(other),
				"after round-tripping kind %s the rung also answers %s — it became a catch-all", kind, other)
		}
	}
}
