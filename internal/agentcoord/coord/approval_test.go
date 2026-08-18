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

// currentRunID reads the harp's CURRENT run_id off the fold (mirrors
// resume_session_capture_test.go's harnessSessionID) — used to prove a
// resume actually minted a fresh run_id for the same harp, the shape
// one-shot's per-turn resume produces on every delivery.
func currentRunID(c *Coordinator, harp string) string {
	var id string
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			id = r.RunID
		}
	})
	return id
}

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
	return planPresetSpawnerFor("", mk)
}

// planPresetSpawnerFor is planPresetSpawner with a settable backend label
// (Wave C3 backend-parity variant) — "" keeps the default ("mock").
func planPresetSpawnerFor(backend string, mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, backend: backend},
	}, nil)
	sp.nextChat = mk
	return sp
}

// relayLadderSpawner builds a fakeSpawner with one migrated agent "worker"
// whose ladder has exactly one ActionRelayToRole rung (timeout given
// explicitly by the caller) — the explicit-ladder vehicle every RELAY-
// MECHANISM test (mailbox landing, reply decode, ACCEPT_FOR_SESSION
// caching, slot-yield contention) uses as of marauding-hacksaw: the plan
// PRESET no longer relays a COMMAND_EXECUTION directly (presetLadder now
// surfaces to a human FIRST — see ladder.go), so a test whose actual
// subject is the relay mechanism itself, not which preset selects it,
// builds the relay rung explicitly here, exactly as an agent's own
// escalation: block would (buildLadder's explicit path is untouched by the
// preset change — TestBuildLadder_ExplicitRelayOnlyOmitsPresetSurfaceRung).
func relayLadderSpawner(mk func() *scriptedChat, timeout time.Duration) *fakeSpawner {
	return relayLadderSpawnerFor("", mk, timeout)
}

// relayLadderSpawnerFor is relayLadderSpawner with a settable backend label
// (Wave C3 backend-parity variant) — "" keeps the default ("mock").
func relayLadderSpawnerFor(backend string, mk func() *scriptedChat, timeout time.Duration) *fakeSpawner {
	ladder := Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: timeout}}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, backend: backend, ladder: ladder},
	}, nil)
	sp.nextChat = mk
	return sp
}

// TestApproval_RelayRoundTrip pins the core C2 flow both ways: a child under
// an explicit relay_to_role-only ladder (relayLadderSpawner — the preset no
// longer relays a COMMAND_EXECUTION directly since marauding-hacksaw;
// TestPresetLadder_PlanSurfacesToHumanFirst pins THAT flow) relays its
// COMMAND_EXECUTION to the parent's mailbox with a correlation id and the
// ApprovalRequest's proto projection, the parent answers via
// agent_send(in_reply_to, structured), and the child's engine receives the
// matching option — proceeding on ACCEPT, declining on DECLINE. The journal
// shows the full chain: the runner's own InteractionRecorded item (counted)
// plus the coordinator's per-rung audit trail (relay_to_role →
// granted/denied), which is where "which rung answered" is actually
// queryable (facts.go's audit journal, no projection — read straight off
// disk, matching the live operational rule).
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
			sp := relayLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
			c := newTestCoordinator(t, sp, nil)

			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
			require.NoError(t, err)

			// The relay lands in the parent's mailbox with a correlation id
			// (the message id) and the request's proto projection.
			msgs := recvKind(t, c, "approval_request", conformanceWait)
			require.Len(t, msgs, 1, "the relay must land in the parent's mailbox")
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

// TestApproval_RelayRoundTrip_BackendParity pins Wave C3's approval-forwarding
// acceptance: the SAME relay round-trip TestApproval_RelayRoundTrip proves
// for claude/mock also holds for codex and kiro — ForwardPermissions
// (decodeHarnessSpec) and the escalation ladder (approval.go) never branch
// on the harness/backend name, only on ApprovalKind, so a relay_to_role
// rung's COMMAND_EXECUTION relays and resolves identically regardless of
// which backend label the run carries.
func TestApproval_RelayRoundTrip_BackendParity(t *testing.T) {
	for _, backend := range []string{"codex", "kiro"} {
		t.Run(backend, func(t *testing.T) {
			resetStrictness(t)
			permReq := commandExecRequest("perm-1")
			sp := relayLadderSpawnerFor(backend, func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
			c := newTestCoordinator(t, sp, nil)

			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
			require.NoError(t, err)

			msgs := recvKind(t, c, "approval_request", conformanceWait)
			require.Len(t, msgs, 1, "backend %q: the relay must land in the parent's mailbox", backend)
			msg := msgs[0]
			assert.Equal(t, "approval_request", msg.Kind)
			assert.Contains(t, string(msg.Structured), "APPROVAL_KIND_COMMAND_EXECUTION")

			decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
			require.NoError(t, err)
			_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "reviewed", decision, msg.ID)
			require.NoError(t, err)

			require.Eventually(t, func() bool {
				sc := sp.chat(0)
				return sc != nil && len(sc.recordedAnswers()) == 1
			}, conformanceWait, 10*time.Millisecond, "backend %q: the child must receive the coordinator's decision", backend)
			ans := sp.chat(0).recordedAnswers()[0]
			assert.Equal(t, "allow-1", ans.OptionID)
		})
	}
}

// TestApproval_TimeoutFallsThroughToCancel pins the fallback chain: a
// relay_to_role rung whose parent never answers times out, and — with
// nothing left in the ladder to catch it — bottoms at CANCEL. The audit
// journal shows both hops: "timed_out" on the relay rung, then "cancelled"
// at the bottom.
//
// CANCEL, not DECLINE: reaching the bottom means NO rung
// resolved — every one of them timed out or failed. Nobody decided anything,
// so the engine is told the request was cancelled. A DECLINE here was
// ctxloom's own refusal wearing the operator's face: claude-code-acp renders
// a reject_once option as {behavior:"deny", message:"User refused permission
// to run tool"} into the model's durable transcript. A rung that genuinely
// declines (auto_decline, or a human/parent answering DECISION_DECLINE) is a
// different fact and still says "refused" — see
// TestApproval_PlanPresetAutoDeclinesFileChange and
// TestApproval_SurfaceToHumanTimeout, which pin that boundary.
func TestApproval_TimeoutFallsThroughToCancel(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-timeout")
	ladder := Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: 50 * time.Millisecond}}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{permission: permReq} }
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	// The relay still lands as mail (it happened), but the parent never
	// answers — the rung's 50ms timeout must fire and fall through.
	require.Len(t, recvKind(t, c, "approval_request", conformanceWait), 1)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the ladder must eventually answer, even with no reply")
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Empty(t, ans.OptionID,
		"bottoming with nobody having decided picks NO option — the engine's cancelled no-op, not a reject option that reads as the operator's refusal")

	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 2, "one timed_out hop, then the bottom CANCEL")
	assert.Equal(t, "timed_out", entries[0].Detail["resolution"])
	assert.Equal(t, "relay_to_role", entries[0].Detail["action"])
	assert.Equal(t, "bottom", entries[1].Detail["rung"])
	assert.Equal(t, "cancelled", entries[1].Detail["resolution"],
		"the audit journal must say what the engine was actually told")
}

// TestApproval_AcceptForSessionSuppressesSecondAsk pins ACCEPT_FOR_SESSION
// caching: once the parent grants it for one kind on a run, a LATER
// like-kind request on the SAME run never reaches the mailbox again — it is
// answered from the coordinator's cache.
func TestApproval_AcceptForSessionSuppressesSecondAsk(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-first")
	sp := relayLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	msgs := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, msgs, 1)

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
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "run another command", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(sp.chat(0).recordedAnswers()) == 2
	}, conformanceWait, 10*time.Millisecond, "the cached grant must answer the second ask")
	second := sp.chat(0).recordedAnswers()[1]
	assert.Equal(t, "perm-second", second.ID)
	assert.Equal(t, "allow-1", second.OptionID)

	// It never went back to the mailbox: no new approval_request pending.
	assertNoMailKind(t, c, "approval_request", 100*time.Millisecond)

	// Audit proof: the second resolution came from "cache", not a rung walk.
	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 2)
	assert.Equal(t, "cache", entries[1].Detail["rung"])
	assert.Equal(t, "granted", entries[1].Detail["resolution"])
}

// TestApproval_AcceptForSessionSurvivesRunIDChange is the Slice 1
// (fix/accept-session-scope) regression proof: ACCEPT_FOR_SESSION must be
// scoped to the harp — the child's whole delegated session — not to the
// runID of the turn that happened to be live when the grant was made. Under
// the coming one-shot model (one-shot-resume.plan.md Fork 2.1) EVERY turn is
// a fresh runID for the same harp, so a runID-scoped cache is wiped at every
// turn boundary and the human is silently re-prompted for something they
// already said "don't ask again" to.
//
// This drives the REAL resume path (not a synthetic key swap): kill the
// engine out from under the run (runner-loss, exactly like
// TestStartRun_ResumeUsesJournaledHarnessSessionID), which ends the run with
// no pending mail — the general "later agent_send resumes an idle Ended
// harp" case (driveQueued's StateEnded branch), not merely the narrow
// immediate-resume-inside-terminateRun race window. The resumed run's FIRST
// turn is scripted plain (no permission) so the test can wait for the fresh
// run to be genuinely up (round-tripped to Idle) before arming the SECOND
// like-kind ask on its SECOND turn — avoiding an unrelated race where a
// permission fired the instant a just-resumed engine's plane-2 channel has
// not yet attached (same reason TestApproval_AcceptForSessionSuppressesSecondAsk
// arms its second ask only after the first has resolved, not on the very
// first token). The second ask's run_id (sp.chatCount() 1->2, currentRunID
// changed) is what proves the runID actually changed under the same harp.
func TestApproval_AcceptForSessionSurvivesRunIDChange(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-first")
	sp := relayLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)
	firstRunID := out.RunID

	msgs := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, msgs, 1)

	decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT_FOR_SESSION"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "go ahead, and for the rest of the session too", decision, msgs[0].ID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1 && sc.recordedAnswers()[0].OptionID == "allow-1"
	}, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	// End the run out from under the child (runner loss — no clean
	// chat-close, no pending mail at terminal): the harp lands StateEnded
	// with its FIRST run_id, exactly the shape driveQueued's later-resume
	// branch (not terminateRun's own immediate mail-race resume) will see on
	// the next delivery.
	sp.killEngine(0)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)

	// Resume with a PLAIN first turn (no permission scripted yet) — this
	// mints a genuinely fresh run_id for the same harp (one-shot's per-turn
	// shape).
	sp.nextChat = func() *scriptedChat { return &scriptedChat{} }
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "run another command", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sp.chatCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"the resume must spawn a genuinely new run (fresh run_id) for the same harp")
	require.Eventually(t, func() bool {
		sc := sp.chat(1)
		return sc != nil && len(sc.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond, "the resumed run's first (plain) turn must round-trip")
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond,
		"the resumed run must settle to idle before its plane-2 channel is exercised again")
	secondRunID := currentRunID(c, out.Harp)
	assert.NotEqual(t, firstRunID, secondRunID, "the resume must be a fresh run_id, not a reused one")

	// NOW arm the second like-kind ask, on the resumed run's SECOND turn —
	// its channel is long since attached (it already round-tripped one turn
	// to idle above).
	sp.chat(1).arm(commandExecRequest("perm-second"))
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "run another command still", nil, "")
	require.NoError(t, err)

	// The cache check is a coordinator-local, synchronous map lookup — a
	// cache HIT resolves in microseconds. A cache MISS (today's runID-keyed
	// bug: the grant lives under firstRunID, which is long gone) instead
	// walks the plan-preset ladder and relays to the parent's mailbox — a
	// SEPARATE approval_request that would sit there for up to 24h waiting
	// for a human this test never provides. So the mailbox is the fast,
	// unambiguous RED signal: check it BEFORE waiting on an answer that a
	// broken cache will never produce.
	assertNoMailKind(t, c, "approval_request", 300*time.Millisecond)

	// The cached grant must answer the second ask WITHOUT a second relay to
	// the parent's mailbox — this is exactly what a runID-scoped cache gets
	// wrong: the grant was cached under firstRunID and firstRunID is gone.
	require.Eventually(t, func() bool {
		return len(sp.chat(1).recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the cached grant must answer the second ask on the NEW run_id without a human re-prompt")
	second := sp.chat(1).recordedAnswers()[0]
	assert.Equal(t, "perm-second", second.ID)
	assert.Equal(t, "allow-1", second.OptionID, "harp-scoped ACCEPT_FOR_SESSION must survive the runID change")
	assert.Equal(t, secondRunID, currentRunID(c, out.Harp), "still the same (resumed) run — no further respawn happened")
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

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond)
	ans := sp.chat(0).recordedAnswers()[0]
	assert.Equal(t, "allow-1", ans.OptionID)

	// No mail was ever relayed to the parent.
	assertNoMailKind(t, c, "approval_request", 100*time.Millisecond)

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

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "edit a file", "", "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, "reject-1", sp.chat(0).recordedAnswers()[0].OptionID)

	assertNoMailKind(t, c, "approval_request", 100*time.Millisecond)

	entries := readApprovalAudit(t, c)
	require.Len(t, entries, 1)
	assert.Equal(t, "auto_decline", entries[0].Detail["action"])
}

// TestLegacyChild_UnaffectedByMigratedSiblingApprovalLadder is the Wave C4
// opportunistic pickup of C2 residual 3: C2 only proved the escalation
// ladder never touches the legacy go-plugin Chat dial via a STRUCTURAL
// zero-diff argument (the ladder lives entirely inside the StartRun/
// RunChannel machinery a legacy child never attaches to), not a runtime
// test. C4's kill-list verification confirms a legacy consumer still
// exists post-kill (the degraded no-reach-back fallback + any
// non-allowlisted StructuredChat backend — see delegate.go's Start doc), so
// the test is not moot: it spawns a LEGACY (non-StartRun) child alongside a
// MIGRATED child that walks a full explicit relay_to_role approval-relay
// round trip (relayLadderSpawner's vehicle — the plan preset itself no
// longer relays directly since marauding-hacksaw), and pins that the two
// run entirely independently — the legacy child never touches the plane-1
// approval item journal, keeps taking turns unaffected by its sibling's
// ladder activity, and the migrated child's relay resolves exactly as
// TestApproval_RelayRoundTrip proves in isolation.
func TestLegacyChild_UnaffectedByMigratedSiblingApprovalLadder(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-legacy-sibling")
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true,
			ladder: Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: conformanceWait}}},
		// "legacy" declares no viaStartRun (zero value: false) — it rides
		// fakeSpawner.Launch, the legacy go-plugin Chat dial's test analog.
		"legacy": {perm: "bypass", profiles: []string{"p1"}},
	}, func() *fakeEngine { return &fakeEngine{} })
	sp.nextChat = func() *scriptedChat { return &scriptedChat{permission: permReq} }
	c := newTestCoordinator(t, sp, nil)

	legacyOut, err := c.AgentRun(context.Background(), ownerIdentity(), "legacy", "hello", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond, "the legacy child must take its first turn")

	workerOut, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)
	msgs := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, msgs, 1, "the migrated child's relay must land in the parent's mailbox")
	msg := msgs[0]
	assert.Equal(t, "approval_request", msg.Kind)

	decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), workerOut.Harp, "", "reviewed", decision, msg.ID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the migrated child must receive the coordinator's decision")
	assert.Equal(t, "allow-1", sp.chat(0).recordedAnswers()[0].OptionID)

	// The legacy child never touched the plane-1 approval item journal (that
	// fold is keyed by run_id; only the migrated child's StartRun run ever
	// wrote to it) and keeps taking turns, unaffected by its sibling's ladder
	// activity.
	c.items.View(func() {
		assert.Zero(t, c.itemsF.countsFor(legacyOut.RunID)["interaction"],
			"the legacy child never rides the plane-1 approval item journal")
	})
	_, err = c.AgentSend(ownerIdentity(), legacyOut.Harp, KindMessage, "second turn", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return len(sp.engine(0).recordedTexts()) == 2
	}, conformanceWait, 10*time.Millisecond, "the legacy child keeps taking turns independent of its migrated sibling's ladder activity")
}

// TestApproval_ReplyRacingTheRelayResolves closes pulpy-whiff: relayApproval
// used to queue the relay mail FIRST and register the pendingApproval second.
// The mailbox message id is the correlation a reply carries, and the mail is
// observable the instant it is queued — so a parent that drained, decided and
// replied inside that window hit resolveApprovalReply before the approval
// existed. The reply then fell through to ordinary mail ("queued mid-turn"),
// the decision was LOST, and the ladder rung eventually timed out and
// DECLINED an action the human had actually approved.
//
// Under contention this reproduced as a flake in the relay round-trip tests.
// Widening those timeouts would have hidden it: the window is not a slow
// coordinator, it is an ordering bug, and a human's approval silently
// becoming a denial is the failure that matters.
func TestApproval_ReplyRacingTheRelayResolves(t *testing.T) {
	resetStrictness(t)
	permReq := commandExecRequest("perm-1")
	sp := relayLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
	c := newTestCoordinator(t, sp, nil)

	// Reply at the earliest instant the correlation id exists: the moment the
	// relay mail becomes observable. If registration does not already cover
	// this id, the decision is dropped.
	var (
		replyDisp string
		replyErr  error
		replied   = make(chan struct{})
	)
	c.onApprovalMailQueued = func(msgID string) {
		decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
		require.NoError(t, err)
		replyDisp, replyErr = c.AgentSend(ownerIdentity(), "", "", "reviewed", decision, msgID)
		close(replied)
	}

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Harp)

	select {
	case <-replied:
	case <-time.After(conformanceWait):
		t.Fatal("the approval relay never queued its mail")
	}
	require.NoError(t, replyErr)
	assert.Contains(t, replyDisp, "DECISION_ACCEPT",
		"a reply arriving the instant the correlation id exists must resolve the approval, not fall through to ordinary mail")

	// And the child actually proceeds: the decision reached the engine.
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the child must receive the approval decision")
	assert.Equal(t, "allow-1", sp.chat(0).recordedAnswers()[0].OptionID)
}
