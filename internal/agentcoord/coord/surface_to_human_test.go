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
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// C2 slice 2 acceptance: an ActionSurfaceToHuman rung — today unreachable
// from any preset (presetLadder never emits it; only an explicit
// escalation: block can, per approval-surface.plan.md's survey), so these
// tests build the ladder directly via fakeAgent.ladder, mirroring
// TestApproval_RelayRoundTrip's shape but for the HUMAN surface: no mail is
// ever queued, PendingApprovals()/OnPendingApproval are the only way an
// observer learns the approval exists, and a human answers over the SAME
// AgentSend(in_reply_to) path a relayed approval's parent answers over.

// surfaceLadderSpawner builds a fakeSpawner with one migrated agent "worker"
// whose ladder has exactly one ActionSurfaceToHuman rung (timeout given
// explicitly by the caller — short for the timeout test, long enough not to
// race the happy path in the round-trip test).
func surfaceLadderSpawner(mk func() *scriptedChat, timeout time.Duration) *fakeSpawner {
	ladder := Ladder{{Action: ActionSurfaceToHuman, Role: ParentAddress, Timeout: timeout}}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
	}, nil)
	sp.nextChat = mk
	return sp
}

// TestApproval_SurfaceToHumanRoundTrip pins the core surfaceApprovalToHuman
// flow both ways: a surface_to_human child's COMMAND_EXECUTION parks (no
// mail queued — assertNoMailKind), PendingApprovals()/OnPendingApproval both
// report it while parked, a human answers via the SAME AgentSend(in_reply_to)
// path a relayed approval's parent answers over, and the child's engine
// receives the matching option. The audit trail names the rung
// (surface_to_human) and its resolution, exactly as relay_to_role's does.
func TestApproval_SurfaceToHumanRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name        string
		decision    string
		wantOption  string
		wantAllowed bool
	}{
		{name: "accept", decision: "DECISION_ACCEPT", wantOption: "allow-1", wantAllowed: true},
		{name: "decline", decision: "DECISION_DECLINE", wantOption: "reject-1", wantAllowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			var sink bytes.Buffer
			restore := clidiag.SetSink(&sink)
			defer restore()

			permReq := commandExecRequest("perm-1")
			sp := surfaceLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
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

			// The callback fires while parked, BEFORE any mail exists — there
			// never is any (assertNoMailKind below), unlike relay_to_role.
			var p PendingApproval
			select {
			case p = <-parked:
			case <-time.After(conformanceWait):
				t.Fatal("OnPendingApproval never fired for a parked human-surfaced approval")
			}
			assert.Equal(t, out.Harp, p.Harp, "PendingApproval names the CHILD run asking, not the human answering it")
			assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION, p.Kind)
			assert.Equal(t, "bash", p.Title)
			assert.NotEmpty(t, p.MessageID, "the message id IS the correlation the human's reply answers")
			assert.False(t, p.Since.IsZero())

			// LOUD: the park itself must warn, naming the harp and the tool —
			// not just become observable to a caller that happens to be
			// watching PendingApprovals()/OnPendingApproval.
			assert.Contains(t, sink.String(), "waiting for a human", "parking for a human must warn, not just journal/callback silently")
			assert.Contains(t, sink.String(), "bash", "the park warning must name the tool that is waiting")

			// PendingApprovals() reports the SAME entry while it is still parked.
			listed := c.PendingApprovals()
			require.Len(t, listed, 1)
			assert.Equal(t, p.MessageID, listed[0].MessageID)

			// A human answers with agent_send in_reply_to carrying an
			// ApprovalDecision projection — the SAME path a relayed approval's
			// parent answers over (resolveApprovalReply/peerSend), nothing new.
			decision, err := json.Marshal(map[string]any{"decision": tc.decision, "note": "reviewed by a human"})
			require.NoError(t, err)
			disp, err := c.AgentSend(ownerIdentity(), out.Harp, "", "reviewed", decision, p.MessageID)
			require.NoError(t, err)
			assert.Contains(t, disp, tc.decision)

			// The child's engine gets the matching option and proceeds/declines.
			require.Eventually(t, func() bool {
				sc := sp.chat(0)
				return sc != nil && len(sc.recordedAnswers()) == 1
			}, conformanceWait, 10*time.Millisecond, "the child must receive the coordinator's decision")
			ans := sp.chat(0).recordedAnswers()[0]
			assert.Equal(t, "perm-1", ans.ID)
			assert.Equal(t, tc.wantOption, ans.OptionID)

			// No approval_request mail was EVER queued — the human surface
			// bypasses the mailbox entirely (relay_to_role's TestApproval_
			// RelayRoundTrip asserts the OPPOSITE for the same shaped request).
			assertNoMailKind(t, c, "approval_request", 200*time.Millisecond)

			// Resolved: PendingApprovals() no longer lists it.
			assert.Empty(t, c.PendingApprovals(), "an answered approval must leave the pending set")

			// The audit trail names the rung (surface_to_human) and its
			// resolution — same keys relay_to_role's audit entry uses.
			entries := readApprovalAudit(t, c)
			require.NotEmpty(t, entries)
			last := entries[len(entries)-1]
			assert.Equal(t, "surface_to_human", last.Detail["action"])
			assert.Equal(t, ParentAddress, last.Detail["role"])
			assert.Equal(t, tc.decision, last.Detail["decision"])
			wantResolution := "granted"
			if !tc.wantAllowed {
				wantResolution = "denied"
			}
			assert.Equal(t, wantResolution, last.Detail["resolution"])
		})
	}
}

// TestApproval_SurfaceToHumanTimeout pins the fall-through-on-timeout path:
// nobody answers, the rung expires, the ladder falls through to the next
// matching rung (auto_decline, bottoming the walk), and the expiry is LOUD —
// a clidiag warning names the harp and the tool, which is exactly what 24
// silent relay_to_role expirations over ten days did NOT get (the plan's
// "make the park and the timeout loud").
func TestApproval_SurfaceToHumanTimeout(t *testing.T) {
	resetStrictness(t)
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	permReq := commandExecRequest("perm-1")
	ladder := Ladder{
		{Action: ActionSurfaceToHuman, Role: ParentAddress, Timeout: 50 * time.Millisecond},
		{Action: ActionAutoDecline},
	}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{permission: permReq} }
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	// Nobody ever answers. The rung's 50ms timeout fires, the ladder falls
	// through to the auto_decline rung beneath it, and the engine gets a
	// reject option (never a hang — the ladder's own "never a hang" doc).
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "a timed-out surface_to_human rung must still fall through and answer the engine")
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Equal(t, "perm-1", ans.ID)
	assert.Equal(t, "reject-1", ans.OptionID, "bottomed at the auto_decline rung beneath the timed-out surface_to_human rung")

	// The audit trail shows BOTH rungs: the timed-out surface_to_human, then
	// the auto_decline that answered.
	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 2)
	assert.Equal(t, "surface_to_human", entries[0].Detail["action"])
	assert.Equal(t, "timed_out", entries[0].Detail["resolution"])
	assert.Equal(t, "auto_decline", entries[1].Detail["action"])
	assert.Equal(t, "denied", entries[1].Detail["resolution"])

	// The timed-out entry must leave the pending set (it is no longer
	// awaiting anyone).
	assert.Empty(t, c.PendingApprovals())

	// LOUD: a human reading stderr, not just the audit journal, must be able
	// to see this happened — naming the harp and the tool.
	assert.Contains(t, sink.String(), "timed out", "the expiry must warn, not just journal silently")
	assert.Contains(t, sink.String(), "bash", "the warning must name the tool that was waiting")
}
