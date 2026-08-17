package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestPresetLadder_BypassAcceptsEverything pins the preset translation:
// bypass = a single catch-all auto_accept rung.
func TestPresetLadder_BypassAcceptsEverything(t *testing.T) {
	l := presetLadder(agent.PermissionBypass)
	require.Len(t, l, 1)
	assert.Equal(t, ActionAutoAccept, l[0].Action)
	assert.Empty(t, l[0].Kinds, "empty Kinds is the catch-all")
	for _, k := range []agentcoordpb.ApprovalRequest_ApprovalKind{
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION,
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE,
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_CUSTOM,
	} {
		rungs := l.matchingRungs(k)
		require.Len(t, rungs, 1)
		assert.Equal(t, ActionAutoAccept, rungs[0].Action)
	}
}

// TestPresetLadder_PlanDeclinesMutatingSurfacesThenRelaysTheRest pins the
// plan preset's post-marauding-hacksaw shape: FILE_CHANGE/
// PERMISSION_ESCALATION auto-decline outright (untouched by the change);
// everything else (including the ambiguous COMMAND_EXECUTION) first
// SURFACES to a human at a live terminal (presetSurfaceTimeout, NOT
// defaultRelayTimeout), then — unanswered — falls through to the ORIGINAL
// relay_to_role rung, unchanged. Was
// TestPresetLadder_PlanDeclinesMutatingRelaysTheRest, which pinned the
// pre-change defect this ladder shipped with: 24/24 preset relay_to_role
// approvals over a month timed out unanswered because the preset relayed
// straight to a parent ROLE with no human-facing surface at all.
func TestPresetLadder_PlanDeclinesMutatingSurfacesThenRelaysTheRest(t *testing.T) {
	l := presetLadder(agent.PermissionPlan)
	for _, k := range []agentcoordpb.ApprovalRequest_ApprovalKind{
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE,
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_PERMISSION_ESCALATION,
	} {
		rungs := l.matchingRungs(k)
		require.NotEmpty(t, rungs)
		assert.Equal(t, ActionAutoDecline, rungs[0].Action, "kind %v must decline outright", k)
	}
	for _, k := range []agentcoordpb.ApprovalRequest_ApprovalKind{
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION,
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL_USE,
		agentcoordpb.ApprovalRequest_APPROVAL_KIND_ARTIFACT_REVIEW,
	} {
		rungs := l.matchingRungs(k)
		require.Len(t, rungs, 2, "kind %v must surface to a human, then fall through to relay", k)
		assert.Equal(t, ActionSurfaceToHuman, rungs[0].Action, "kind %v must surface to a human FIRST", k)
		assert.Equal(t, ParentAddress, rungs[0].Role)
		// Pinned against the LITERAL value, not the presetSurfaceTimeout
		// symbol: a mutant that redefines the constant to defaultRelayTimeout
		// must still turn this assertion red, which comparing against the
		// symbol itself could never do.
		assert.Equal(t, 10*time.Minute, rungs[0].Timeout,
			"the preset surface rung must use presetSurfaceTimeout (10m), not defaultRelayTimeout (24h)")
		assert.Equal(t, presetSurfaceTimeout, rungs[0].Timeout, "sanity: the constant itself flows through unmodified")
		assert.Equal(t, ActionRelayToRole, rungs[1].Action, "kind %v must still relay for judgment if nobody answers", k)
		assert.Equal(t, ParentAddress, rungs[1].Role)
		assert.Equal(t, defaultRelayTimeout, rungs[1].Timeout, "the relay rung beneath the surface rung is unchanged")
	}
}

// TestBuildLadder_ExplicitOverridesPreset pins the override rule: a
// non-empty declared escalation: REPLACES the preset entirely, no merge.
func TestBuildLadder_ExplicitOverridesPreset(t *testing.T) {
	raw := []agents.EscalationRung{
		{Kinds: []string{"FILE_CHANGE"}, Action: "auto_accept"}, // the OPPOSITE of the plan preset
	}
	l, err := buildLadder("custom-agent", raw, agent.PermissionPlan)
	require.NoError(t, err)
	require.Len(t, l, 1)
	rungs := l.matchingRungs(agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE)
	require.Len(t, rungs, 1)
	assert.Equal(t, ActionAutoAccept, rungs[0].Action, "an explicit escalation: must override the preset, not merge with it")
	// A kind the custom ladder never mentions matches nothing (bottoms at
	// DECLINE at serveApproval, not presetLadder's catch-all).
	assert.Empty(t, l.matchingRungs(agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION))
}

// TestBuildLadder_TimeoutParsed pins Go-duration timeout parsing on a relay
// rung, and the default when one is omitted.
func TestBuildLadder_TimeoutParsed(t *testing.T) {
	raw := []agents.EscalationRung{
		{Action: "relay_to_role", Timeout: "90s"},
		{Action: "surface_to_human"},
	}
	l, err := buildLadder("a", raw, agent.PermissionPlan)
	require.NoError(t, err)
	require.Len(t, l, 2)
	assert.Equal(t, 90*time.Second, l[0].Timeout)
	assert.Equal(t, defaultRelayTimeout, l[1].Timeout, "an omitted timeout uses the resolver's default")
	assert.Equal(t, ParentAddress, l[1].Role, "role defaults to parent when unset")
}

// TestBuildLadder_ValidationErrors pins the fail-loud config-schema
// validation: an implementer or a hand-edited agent YAML gets a clear
// error, never a silently-wrong ladder.
func TestBuildLadder_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		rung agents.EscalationRung
	}{
		{"unknown action", agents.EscalationRung{Action: "maybe"}},
		{"unknown kind", agents.EscalationRung{Action: "auto_accept", Kinds: []string{"NOT_A_KIND"}}},
		{"role on auto_accept", agents.EscalationRung{Action: "auto_accept", Role: "parent"}},
		{"role on auto_decline", agents.EscalationRung{Action: "auto_decline", Role: "parent"}},
		{"unaddressable role", agents.EscalationRung{Action: "relay_to_role", Role: "some-sibling"}},
		{"bad timeout", agents.EscalationRung{Action: "relay_to_role", Timeout: "not-a-duration"}},
		{"negative timeout", agents.EscalationRung{Action: "relay_to_role", Timeout: "-5s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildLadder("a", []agents.EscalationRung{tc.rung}, agent.PermissionPlan)
			require.Error(t, err)
		})
	}
}

// TestLadder_JournalRoundTrip pins the durable fact projection (ladderToFact
// / ladderFromFact) — the run registry journals the resolved ladder at
// enqueue (children.go) and restart adoption must recover it byte-faithful.
func TestLadder_JournalRoundTrip(t *testing.T) {
	l := Ladder{
		{Kinds: []agentcoordpb.ApprovalRequest_ApprovalKind{
			agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE,
			agentcoordpb.ApprovalRequest_APPROVAL_KIND_PERMISSION_ESCALATION,
		}, Action: ActionAutoDecline},
		{Action: ActionRelayToRole, Role: ParentAddress, Timeout: 90 * time.Second},
	}
	got := ladderFromFact(ladderToFact(l))
	require.Len(t, got, 2)
	assert.Equal(t, l[0].Action, got[0].Action)
	assert.ElementsMatch(t, l[0].Kinds, got[0].Kinds)
	assert.Equal(t, l[1].Action, got[1].Action)
	assert.Equal(t, l[1].Role, got[1].Role)
	assert.Equal(t, l[1].Timeout, got[1].Timeout)
}

// TestBuildLadder_NoEscalationConfigDefaultsRelayTo24Hours pins vain-cough
// item 1's default flip: an agent that declares no escalation: block at all
// (the common case) gets a relay rung that waits 24 HOURS before falling
// through, not the old 5-minute human-SLA default. The whole point is that a
// busy coordinator that hasn't called agent_recv yet must not have its
// child's request auto-DENIED out from under it — see the approval-drop
// cluster this default caused.
func TestBuildLadder_NoEscalationConfigDefaultsRelayTo24Hours(t *testing.T) {
	l, err := buildLadder("a", nil, agent.PermissionPlan)
	require.NoError(t, err)
	rungs := l.matchingRungs(agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION)
	require.NotEmpty(t, rungs)
	relay := rungs[len(rungs)-1]
	require.Equal(t, ActionRelayToRole, relay.Action)
	assert.Equal(t, 24*time.Hour, relay.Timeout,
		"an agent with no escalation: config must wait 24h before falling through (auto-denying), not the old 5m default")
}

// TestBuildLadder_EscalationConfigOverridesDefaultRelayTimeout pins
// vain-cough item 1's "agent configurable" half: an agent that DOES declare
// an escalation: block with its own relay timeout gets THAT value, not the
// 24h default — buildLadder already parses EscalationRung.Timeout for a
// relay/surface rung (this pins that the override still wins after the
// default changes, and documents the mechanism, not merely the number).
func TestBuildLadder_EscalationConfigOverridesDefaultRelayTimeout(t *testing.T) {
	raw := []agents.EscalationRung{
		{Action: "relay_to_role", Timeout: "10m"},
	}
	l, err := buildLadder("a", raw, agent.PermissionPlan)
	require.NoError(t, err)
	require.Len(t, l, 1)
	assert.Equal(t, 10*time.Minute, l[0].Timeout,
		"an agent's own escalation: timeout must override the 24h default")
	assert.NotEqual(t, 24*time.Hour, l[0].Timeout)
}

// TestBuildLadder_ExplicitRelayOnlyOmitsPresetSurfaceRung is
// marauding-hacksaw's explicit-escalation regression proof: presetLadder's
// plan preset now injects a surface_to_human rung ahead of its relay rung,
// but buildLadder's explicit path (a non-empty raw escalation: block)
// REPLACES the preset wholesale (TestBuildLadder_ExplicitOverridesPreset) —
// it must never MERGE the two. An agent that declares its own
// relay_to_role-only ladder gets EXACTLY that: no surface rung anywhere in
// it, exactly as before this change.
func TestBuildLadder_ExplicitRelayOnlyOmitsPresetSurfaceRung(t *testing.T) {
	raw := []agents.EscalationRung{
		{Action: "relay_to_role"},
	}
	l, err := buildLadder("custom-agent", raw, agent.PermissionPlan)
	require.NoError(t, err)
	require.Len(t, l, 1, "an explicit escalation: block must not have a preset surface rung merged into it")
	assert.Equal(t, ActionRelayToRole, l[0].Action)
	for _, r := range l {
		assert.NotEqual(t, ActionSurfaceToHuman, r.Action,
			"the preset's new surface_to_human rung must never leak into an explicit escalation: block")
	}
}
