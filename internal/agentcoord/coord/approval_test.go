package coord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// C2 acceptance: a scripted child under a plan-preset ladder attempts a
// COMMAND_EXECUTION → relayed to the parent's mailbox → parent decision →
// child proceeds/declines accordingly; the journal shows the full
// InteractionRecorded chain (which rung answered is queryable); the timeout
// path falls back to DECLINE; ACCEPT_FOR_SESSION suppresses the second
// like-kind ask.

// auditEntry is c.audit's durable, no-projection payload (facts.go).
type auditEntry struct {
	Kind   string            `json:"kind"`
	Actor  string            `json:"actor,omitempty"`
	Detail map[string]string `json:"detail,omitempty"`
}

// readApprovalAudit reads interactions.jsonl straight off disk (the audit
// journal has no fold/query API by design — facts.go: "an audit log with no
// projection") and returns every "approval" entry, oldest first — the
// operational way to answer "which rung answered".
func readApprovalAudit(t *testing.T, c *Coordinator) []auditEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.stateDir, "interactions.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []auditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var f Fact
		require.NoError(t, json.Unmarshal([]byte(line), &f))
		if f.Kind != "interaction" {
			continue
		}
		var e auditEntry
		require.NoError(t, json.Unmarshal(f.Data, &e))
		if e.Kind == "approval" {
			out = append(out, e)
		}
	}
	return out
}

// commandExecRequest is a scripted COMMAND_EXECUTION-classified permission
// request (ACP's "execute" tool kind), offering the standard four options.
func commandExecRequest(id string) *agent.PermissionRequest {
	return &agent.PermissionRequest{
		ID:        id,
		ToolName:  "bash",
		Kind:      "execute",
		ToolInput: []byte(`{"command":"rm -rf /tmp/scratch"}`),
		Options: []agent.PermissionOption{
			{ID: "allow-1", Kind: "allow_once", Name: "Allow once"},
			{ID: "allow-2", Kind: "allow_always", Name: "Always allow"},
			{ID: "reject-1", Kind: "reject_once", Name: "Reject once"},
			{ID: "reject-2", Kind: "reject_always", Name: "Always reject"},
		},
	}
}

// planPresetSpawner builds a fakeSpawner with one migrated agent "worker"
// under the plan permission preset (buildLadder(nil, plan) — auto-decline
// FILE_CHANGE/PERMISSION_ESCALATION, relay the rest to the parent).
func planPresetSpawner(mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.nextChat = mk
	return sp
}

// TestApproval_RelayRoundTrip pins the core C2 flow both ways: a plan-preset
// child's COMMAND_EXECUTION relays to the parent's mailbox with a
// correlation id and the ApprovalRequest's proto projection, the parent
// answers via agent_send(in_reply_to, structured), and the child's engine
// receives the matching option — proceeding on ACCEPT, declining on
// DECLINE. The journal shows the full chain: the runner's own
// InteractionRecorded item (counted) plus the coordinator's per-rung audit
// trail (relay_to_role → granted/denied), which is where "which rung
// answered" is actually queryable (facts.go's audit journal, no
// projection — read straight off disk, matching the live operational rule).
func TestApproval_RelayRoundTrip(t *testing.T) {
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
			permReq := commandExecRequest("perm-1")
			sp := planPresetSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} })
			c := newTestCoordinator(t, sp, nil)

			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command")
			require.NoError(t, err)

			// The relay lands in the parent's mailbox with a correlation id
			// (the message id) and the request's proto projection.
			var msgs []Message
			require.Eventually(t, func() bool {
				msgs, err = c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
				return err == nil && len(msgs) == 1
			}, conformanceWait, 10*time.Millisecond, "the relay must land in the parent's mailbox")
			msg := msgs[0]
			assert.Equal(t, "approval_request", msg.Kind)
			assert.NotEmpty(t, msg.ID, "the mailbox message id IS the correlation the reply answers")
			require.NotEmpty(t, msg.Structured)
			assert.Contains(t, string(msg.Structured), "APPROVAL_KIND_COMMAND_EXECUTION", "the ACP execute kind classifies as COMMAND_EXECUTION")
			assert.Contains(t, string(msg.Structured), "rm -rf /tmp/scratch", "the proto projection carries the tool input payload")

			// The parent answers with agent_send in_reply_to carrying an
			// ApprovalDecision projection.
			decision, err := json.Marshal(map[string]any{"decision": tc.decision, "note": "reviewed"})
			require.NoError(t, err)
			disp, err := c.AgentSend(ownerIdentity(), out.Harp, "", "reviewed", decision, msg.ID)
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

			// Journal: the runner's own InteractionRecorded item is counted...
			require.Eventually(t, func() bool {
				var counts map[string]int
				c.items.View(func() { counts = c.itemsF.countsFor(out.RunID) })
				return counts["interaction"] == 1
			}, conformanceWait, 10*time.Millisecond, "the InteractionRecorded item must journal")

			// ...and the coordinator's per-rung audit trail names the rung
			// (relay_to_role) and its resolution — "which rung answered" is
			// queryable straight off the audit journal.
			entries := readApprovalAudit(t, c)
			require.NotEmpty(t, entries)
			last := entries[len(entries)-1]
			assert.Equal(t, "relay_to_role", last.Detail["action"])
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

// TestApproval_TimeoutFallsThroughToDecline pins the fallback chain: a
// relay_to_role rung whose parent never answers times out, and — with
// nothing left in the ladder to catch it — bottoms at DECLINE. The audit
// journal shows both hops: "timed_out" on the relay rung, then "denied" at
// the bottom.
func TestApproval_TimeoutFallsThroughToDecline(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-timeout")
	ladder := Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: 50 * time.Millisecond}}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{permission: permReq} }
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command")
	require.NoError(t, err)

	// The relay still lands as mail (it happened), but the parent never
	// answers — the rung's 50ms timeout must fire and fall through.
	require.Eventually(t, func() bool {
		msgs, rerr := c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
		return rerr == nil && len(msgs) == 1
	}, conformanceWait, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the ladder must eventually answer, even with no reply")
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Equal(t, "reject-1", ans.OptionID, "bottoming at DECLINE picks a reject option")

	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 2, "one timed_out hop, then the bottom DECLINE")
	assert.Equal(t, "timed_out", entries[0].Detail["resolution"])
	assert.Equal(t, "relay_to_role", entries[0].Detail["action"])
	assert.Equal(t, "bottom", entries[1].Detail["rung"])
	assert.Equal(t, "denied", entries[1].Detail["resolution"])
}

// TestApproval_AcceptForSessionSuppressesSecondAsk pins ACCEPT_FOR_SESSION
// caching: once the parent grants it for one kind on a run, a LATER
// like-kind request on the SAME run never reaches the mailbox again — it is
// answered from the coordinator's cache.
func TestApproval_AcceptForSessionSuppressesSecondAsk(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-first")
	sp := planPresetSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command")
	require.NoError(t, err)

	var msgs []Message
	require.Eventually(t, func() bool {
		msgs, err = c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
		return err == nil && len(msgs) == 1
	}, conformanceWait, 10*time.Millisecond)

	decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT_FOR_SESSION"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "go ahead, and for the rest of the run too", decision, msgs[0].ID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1 && sc.recordedAnswers()[0].OptionID == "allow-1"
	}, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	// A SECOND like-kind ask, on a new turn.
	sp.chat(0).arm(commandExecRequest("perm-second"))
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "task", "run another command", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(sp.chat(0).recordedAnswers()) == 2
	}, conformanceWait, 10*time.Millisecond, "the cached grant must answer the second ask")
	second := sp.chat(0).recordedAnswers()[1]
	assert.Equal(t, "perm-second", second.ID)
	assert.Equal(t, "allow-1", second.OptionID)

	// It never went back to the mailbox: no new approval_request pending.
	_, err = c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
	assert.ErrorIs(t, err, ErrRecvTimeout, "the second ask must be answered from cache, not relayed again")

	// Audit proof: the second resolution came from "cache", not a rung walk.
	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 2)
	assert.Equal(t, "cache", entries[1].Detail["rung"])
	assert.Equal(t, "granted", entries[1].Detail["resolution"])
}

// TestApproval_BypassPresetAutoAcceptsAll pins the bypass preset (kind-lilac
// compat): every kind auto-accepts immediately, with NO relay to the parent
// at all — mirroring decidePermission's old "allow under bypass" default.
func TestApproval_BypassPresetAutoAcceptsAll(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-bypass")
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: "container", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{permission: permReq} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond)
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Equal(t, "allow-1", ans.OptionID)

	// No mail was ever relayed to the parent.
	_, err = c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
	assert.ErrorIs(t, err, ErrRecvTimeout)

	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 1)
	assert.Equal(t, "auto_accept", entries[0].Detail["action"])
	assert.Equal(t, "granted", entries[0].Detail["resolution"])

	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
}

// TestApproval_PlanPresetAutoDeclinesFileChange pins the other half of the
// plan preset: a mutating kind (FILE_CHANGE) declines OUTRIGHT — no relay,
// no parent involvement — while COMMAND_EXECUTION (ambiguous — could be
// read-only) still relays (TestApproval_RelayRoundTrip).
func TestApproval_PlanPresetAutoDeclinesFileChange(t *testing.T) {
	resetStrictness(t)
	permReq := &agent.PermissionRequest{
		ID: "perm-edit", ToolName: "edit_file", Kind: "edit",
		Options: []agent.PermissionOption{
			{ID: "allow-1", Kind: "allow_once"},
			{ID: "reject-1", Kind: "reject_once"},
		},
	}
	sp := planPresetSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} })
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "edit a file")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, "reject-1", sp.chat(0).recordedAnswers()[0].OptionID)

	_, err = c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
	assert.ErrorIs(t, err, ErrRecvTimeout, "a mutating kind must decline outright, never relay")

	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 1)
	assert.Equal(t, "auto_decline", entries[0].Detail["action"])
}
