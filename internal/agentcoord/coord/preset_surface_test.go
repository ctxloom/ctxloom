package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// marauding-hacksaw: presetLadder's plan preset used to
// relay a COMMAND_EXECUTION approval straight to the parent ROLE, so the
// terminal approval surface (surround-bar ⚠N + BEL +
// overlay y/s/n view, answering via AgentSend -> resolveApprovalReply) was
// reachable only through an explicit escalation: block. Measured evidence:
// 24/24 preset relay_to_role approvals over a month timed out unanswered —
// a zero answer rate. These tests pin the fixed default end to end, driven
// through a REAL AgentRun (ladder_test.go's
// TestPresetLadder_PlanDeclinesMutatingSurfacesThenRelaysTheRest already
// pins the ladder's static SHAPE — presetLadder(plan) inspected directly).

// TestPresetLadder_PlanSurfacesToHumanFirst pins the happy path: a
// plan-PRESET (fakeAgent.ladder left nil — no explicit escalation: block,
// mirroring buildLadder's own "no raw -> presetLadder" branch)
// COMMAND_EXECUTION parks on surface_to_human FIRST — PendingApprovals()/
// OnPendingApproval both report it while no mail is EVER queued (the human
// surface bypasses the mailbox — surfaceApprovalToHuman's own doc) — and a
// human's AgentSend answer resolves it with the engine receiving the
// matching ALLOW option, read off its own recorded answer (a payload
// assertion, not merely a nil error).
func TestPresetLadder_PlanSurfacesToHumanFirst(t *testing.T) {
	resetStrictness(t)
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	permReq := commandExecRequest("perm-1")
	sp := planPresetSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} })
	c := newTestCoordinator(t, sp, nil)

	parked := make(chan PendingApproval, 1)
	c.OnPendingApproval(func(p PendingApproval, isParked bool) {
		if isParked {
			select {
			case parked <- p:
			default:
			}
		}
	})

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	var p PendingApproval
	select {
	case p = <-parked:
	case <-time.After(conformanceWait):
		t.Fatal("a plan-preset COMMAND_EXECUTION must park on surface_to_human FIRST — it never did (relay-first regression)")
	}
	assert.Equal(t, out.Harp, p.Harp, "PendingApproval names the CHILD run asking")
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION, p.Kind)
	assert.Equal(t, "bash", p.Title)
	assert.NotEmpty(t, p.MessageID)
	// Pinned against the LITERAL 10m, not the presetSurfaceTimeout symbol: a
	// mutant that redefines the constant to defaultRelayTimeout must still
	// turn this assertion red.
	assert.Equal(t, p.Since.Add(10*time.Minute), p.Deadline,
		"the preset surface rung's park must use presetSurfaceTimeout (10m), not defaultRelayTimeout (24h)")
	assert.Equal(t, p.Since.Add(presetSurfaceTimeout), p.Deadline, "sanity: the constant itself flows through unmodified")

	require.Len(t, c.PendingApprovals(), 1)

	// No mail was EVER queued while parked — the preset's FIRST rung is now
	// the human surface, not a relay to the mailbox (the opposite of the
	// pre-change default this test replaces the assumptions of).
	assertNoMailKind(t, c, "approval_request", 200*time.Millisecond)

	assert.Contains(t, sink.String(), "waiting for a human", "the park must warn loudly, same as an explicit surface_to_human rung")

	decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed by a human"})
	require.NoError(t, err)
	disp, err := c.AgentSend(ownerIdentity(), out.Harp, "", "reviewed", decision, p.MessageID)
	require.NoError(t, err)
	assert.Contains(t, disp, "DECISION_ACCEPT")

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the child must receive the coordinator's decision")
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Equal(t, "perm-1", ans.ID)
	assert.Equal(t, "allow-1", ans.OptionID,
		"the human's answer must reach the engine as the matching ALLOW option — a payload, not just a nil error")

	assert.Empty(t, c.PendingApprovals(), "an answered approval must leave the pending set")

	entries := readApprovalAudit(t, c)
	require.NotEmpty(t, entries)
	last := entries[len(entries)-1]
	assert.Equal(t, "surface_to_human", last.Detail["action"])
	assert.Equal(t, ParentAddress, last.Detail["role"])
	assert.Equal(t, "granted", last.Detail["resolution"])
}

// TestPresetLadder_SurfaceTimeoutFallsThroughToRelayThenCancel pins the
// preset's full fall-through chain when nobody is home at either hop:
// surface_to_human times out, the ladder falls through to the ORIGINAL
// relay_to_role rung beneath it (unchanged), which also times out, and the
// walk bottoms at CANCEL — nobody decided, so nothing is reported as a
// decision (ladder.go's own "never a hang" doc).
//
// This drives the preset's exact two-rung SHAPE end to end, but with tiny
// timeouts rather than waiting out presetSurfaceTimeout's real 10m /
// defaultRelayTimeout's real 24h: LadderRung.Timeout is the one seam both a
// preset and an explicit escalation: block set, so — consistent with how
// every other timeout test in this package drives the clock (there is no
// separate fake-clock mechanism) — the ladder here is built directly
// (fakeAgent.ladder) with the SAME actions/roles presetLadder(plan) now
// produces for an ambiguous kind, just with millisecond timeouts.
func TestPresetLadder_SurfaceTimeoutFallsThroughToRelayThenCancel(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-timeout")
	ladder := Ladder{
		{Action: ActionSurfaceToHuman, Role: ParentAddress, Timeout: 50 * time.Millisecond},
		{Action: ActionRelayToRole, Role: ParentAddress, Timeout: 50 * time.Millisecond},
	}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{permission: permReq} }
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	// Nobody ever answers either hop. The engine eventually gets a CANCEL
	// (no option at all), never a hang.
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the ladder must eventually answer, even with nobody home at either hop")
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Empty(t, ans.OptionID, "bottoming with nobody having decided picks NO option, never a reject that reads as the operator's refusal")

	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 3, "all three resolutions, in order: surface timeout, relay timeout, bottom cancel")
	assert.Equal(t, "surface_to_human", entries[0].Detail["action"])
	assert.Equal(t, "timed_out", entries[0].Detail["resolution"])
	assert.Equal(t, "relay_to_role", entries[1].Detail["action"])
	assert.Equal(t, "timed_out", entries[1].Detail["resolution"])
	assert.Equal(t, "bottom", entries[2].Detail["rung"])
	assert.Equal(t, "cancelled", entries[2].Detail["resolution"])

	assert.Empty(t, c.PendingApprovals(), "a timed-out surface entry must not linger in the pending set")
}
