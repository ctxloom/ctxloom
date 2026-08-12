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
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// legal-jelly / oily-morse: the approval REPLY decode path. Every test here
// asserts PROGRESS (the rung actually resolved, the journal actually says
// what happened), never EXISTENCE (a call returned, a struct decoded) —
// the whole cluster this file closes shipped green because the suite only
// ever fed well-formed payloads down a path that reports success by default.

// readAuditKind reads interactions.jsonl straight off disk and returns every
// audit entry of the given kind, oldest first (readApprovalAudit's
// kind-parameterised twin — the audit journal has no projection API).
func readAuditKind(t *testing.T, c *Coordinator, kind string) []auditEntry {
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
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// pendingApprovalCount reports how many relayed approvals are still
// outstanding — the registry state a burned correlation destroys.
func pendingApprovalCount(c *Coordinator) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.approvals)
}

// startRelayedApproval spawns a child under an explicit relay_to_role-only
// ladder (relayLadderSpawner — the plan preset itself no longer relays a
// COMMAND_EXECUTION directly since marauding-hacksaw; this file's subject is
// the REPLY DECODE path, which relay_to_role and surface_to_human share
// identically, so the vehicle is the explicit ladder, not the preset) that
// immediately asks for a COMMAND_EXECUTION approval, and returns the
// coordinator, the spawner, the child harp, and the relayed mail's
// correlation id.
func startRelayedApproval(t *testing.T, permID string) (*Coordinator, *fakeSpawner, string, string) {
	t.Helper()
	resetStrictness(t)
	permReq := commandExecRequest(permID)
	sp := relayLadderSpawner(func() *scriptedChat { return &scriptedChat{permission: permReq} }, conformanceWait)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	msgs := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, msgs, 1, "the relay must land in the parent's mailbox")
	require.NotEmpty(t, msgs[0].ID)
	return c, sp, out.Harp, msgs[0].ID
}

// requireChildAnswered asserts the child's engine actually received an
// answer carrying the expected option — the only evidence a decision
// travelled the whole way, as opposed to a send that merely returned.
func requireChildAnswered(t *testing.T, sp *fakeSpawner, wantOption string) {
	t.Helper()
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the child must receive the coordinator's decision")
	assert.Equal(t, wantOption, sp.chat(0).recordedAnswers()[0].OptionID)
}

// TestApproval_MalformedReplyDoesNotBurnTheCorrelation is legal-jelly's core
// red: resolveApprovalReply used to DELETE the pending approval before
// decoding the payload, and the error path never put it back. One malformed
// reply therefore permanently destroyed the correlation — the retry missed,
// degraded silently to ordinary chat mail, and the child sat out its full
// relay timeout before being auto-denied. The decode must consume nothing it
// cannot honour.
func TestApproval_MalformedReplyDoesNotBurnTheCorrelation(t *testing.T) {
	c, sp, harp, msgID := startRelayedApproval(t, "perm-malformed")

	bad, err := json.Marshal(map[string]any{"decision": "ALLOW"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), harp, "", "approved", bad, msgID)
	require.Error(t, err, "a malformed ApprovalDecision must fail the send loudly")
	assert.Contains(t, err.Error(), "DECISION_ACCEPT",
		"the decode error must name the accepted vocabulary, so the failure is self-correcting")

	assert.Equal(t, 1, pendingApprovalCount(c),
		"a failed decode must leave the approval outstanding — it consumed nothing")

	// The retry on the SAME correlation still resolves the rung.
	good, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
	require.NoError(t, err)
	disp, err := c.AgentSend(ownerIdentity(), harp, "", "approved", good, msgID)
	require.NoError(t, err)
	assert.Contains(t, disp, "DECISION_ACCEPT")

	requireChildAnswered(t, sp, "allow-1")
}

// TestApproval_CourtesyAckDoesNotBurnTheCorrelation: no approval INTENT is
// needed to trigger the defect. Any agent_send carrying a live approval's
// in_reply_to is force-decoded as an ApprovalDecision, so a bare courtesy ack
// ({"kind": "result"}) both failed the send AND burned the approval, hanging
// the child for the whole relay timeout.
func TestApproval_CourtesyAckDoesNotBurnTheCorrelation(t *testing.T) {
	c, sp, harp, msgID := startRelayedApproval(t, "perm-ack")

	ack, err := json.Marshal(map[string]any{"kind": "result"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), harp, "result", "noted, deciding shortly", ack, msgID)
	require.Error(t, err, "an ack that carries no decision must fail loudly, naming what was expected")
	assert.Contains(t, err.Error(), "DECISION_ACCEPT")

	assert.Equal(t, 1, pendingApprovalCount(c),
		"a courtesy ack must never consume the approval it was replying to")

	good, err := json.Marshal(map[string]any{"decision": "DECISION_DECLINE", "note": "no"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), harp, "", "declined", good, msgID)
	require.NoError(t, err)
	requireChildAnswered(t, sp, "reject-1")
}

// TestApproval_ReplyWithKindEnvelopeResolves: `kind` is the structured
// payload's own envelope key — both agent_send surfaces document it and
// servePeerSend reads it off the same Struct. Carrying it alongside the
// decision must therefore resolve the approval, not fail decode.
func TestApproval_ReplyWithKindEnvelopeResolves(t *testing.T) {
	c, sp, harp, msgID := startRelayedApproval(t, "perm-kind")

	reply, err := json.Marshal(map[string]any{"kind": "approval_response", "decision": "DECISION_ACCEPT"})
	require.NoError(t, err)
	disp, err := c.AgentSend(ownerIdentity(), harp, "approval_response", "approved", reply, msgID)
	require.NoError(t, err, "the envelope `kind` key must not fail the ApprovalDecision decode")
	assert.Contains(t, disp, "DECISION_ACCEPT")

	requireChildAnswered(t, sp, "allow-1")
}

// TestApproval_OutOfRangeDecisionIsRejected is oily-morse's decode half:
// proto3 open enums mean {"decision": 99} unmarshals WITHOUT error, so a
// junk decision used to sail through, journal itself as "granted" at the
// coordinator, and be enforced as a denial at the child — two audit trails
// disagreeing about one event. A value outside the declared vocabulary is
// rejected at the boundary.
func TestApproval_OutOfRangeDecisionIsRejected(t *testing.T) {
	c, sp, harp, msgID := startRelayedApproval(t, "perm-oob")

	bad, err := json.Marshal(map[string]any{"decision": 99})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), harp, "", "approved", bad, msgID)
	require.Error(t, err, "a decision outside the declared vocabulary must be rejected at decode")
	assert.Contains(t, err.Error(), "DECISION_ACCEPT")

	assert.Equal(t, 1, pendingApprovalCount(c))

	for _, e := range readApprovalAudit(t, c) {
		assert.NotEqual(t, "granted", e.Detail["resolution"],
			"an out-of-range decision must never be journaled as granted")
	}

	good, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), harp, "", "approved", good, msgID)
	require.NoError(t, err)
	requireChildAnswered(t, sp, "allow-1")
}

// TestApproval_FailedReplyDecodeLeavesAnAuditTrace: the failed-decode path
// queued no mail and wrote no fact, so it left ZERO disk trace — which is
// exactly why the live incident could not identify which send burned the
// approval. A rejected reply is journaled like every other hop.
func TestApproval_FailedReplyDecodeLeavesAnAuditTrace(t *testing.T) {
	c, _, harp, msgID := startRelayedApproval(t, "perm-trace")

	bad, err := json.Marshal(map[string]any{"decision": "ALLOW"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), harp, "", "approved", bad, msgID)
	require.Error(t, err)

	entries := readAuditKind(t, c, "approval_reply")
	require.NotEmpty(t, entries, "a rejected approval reply must leave a durable trace")
	last := entries[len(entries)-1]
	assert.Equal(t, "rejected", last.Detail["resolution"])
	assert.Equal(t, msgID, last.Detail["in_reply_to"])
	assert.NotEmpty(t, last.Detail["error"])
}

// TestApproval_ResolvedReplyReportsDelivered pins the delivery-status oracle:
// a real approval match returns delivered=true, so DELIVERY_QUEUED on an
// approval reply is PROOF the reply was not honoured. The regression shape is
// the second send here — after a malformed first attempt, the retry used to
// miss the (already-deleted) registry entry and degrade to ordinary queued
// mail while still reporting success.
func TestApproval_ResolvedReplyReportsDelivered(t *testing.T) {
	c, sp, harp, msgID := startRelayedApproval(t, "perm-delivered")

	badStruct, err := structpb.NewStruct(map[string]any{"decision": "ALLOW"})
	require.NoError(t, err)
	resp := c.servePeerSend(ownerIdentity(), &agentcoordpb.PeerSendRequest{
		ToAgentId: harp, Text: "approved", InReplyTo: msgID, Structured: badStruct,
	})
	assert.NotEqualValues(t, 0, resp.GetStatus().GetCode(),
		"a malformed approval reply must not report OK")

	goodStruct, err := structpb.NewStruct(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
	require.NoError(t, err)
	resp = c.servePeerSend(ownerIdentity(), &agentcoordpb.PeerSendRequest{
		ToAgentId: harp, Text: "approved", InReplyTo: msgID, Structured: goodStruct,
	})
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Equal(t, agentcoordpb.PeerSendResult_DELIVERY_DELIVERED, resp.GetPeerSend().GetDelivery(),
		"a resolved approval reports DELIVERED; DELIVERY_QUEUED means it fell through to ordinary mail")

	requireChildAnswered(t, sp, "allow-1")
}

// TestApprovalResolution_MirrorsEnforcement is oily-morse's journal half.
// The coordinator's audit resolution and the child's own InteractionRecorded
// resolution describe the SAME event, so they must agree for every value of
// the enum — including UNSPECIFIED and an out-of-range numeric. The audit
// journal used to default to "granted" and downgrade only on an explicit
// DECLINE/CANCEL (fail-OPEN), while enforcement is an explicit ACCEPT
// allow-list (fail-CLOSED): the two disagreed on exactly the values nobody
// wrote a test for.
func TestApprovalResolution_MirrorsEnforcement(t *testing.T) {
	want := map[agentcoordpb.InteractionRecorded_Resolution]string{
		agentcoordpb.InteractionRecorded_RESOLUTION_GRANTED:   "granted",
		agentcoordpb.InteractionRecorded_RESOLUTION_DENIED:    "denied",
		agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED: "cancelled",
	}
	for _, d := range []agentcoordpb.ApprovalDecision_Decision{
		agentcoordpb.ApprovalDecision_DECISION_UNSPECIFIED,
		agentcoordpb.ApprovalDecision_DECISION_ACCEPT,
		agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION,
		agentcoordpb.ApprovalDecision_DECISION_DECLINE,
		agentcoordpb.ApprovalDecision_DECISION_CANCEL,
		agentcoordpb.ApprovalDecision_Decision(99),
	} {
		child := interactionResolution(d)
		assert.Equal(t, want[child], approvalResolution(d),
			"decision %v: the coordinator's audit journal and the child's InteractionRecorded must agree", d)
	}
	assert.Equal(t, "denied", approvalResolution(agentcoordpb.ApprovalDecision_Decision(99)),
		"an unknown decision defaults to denied — the audit journal must fail CLOSED like enforcement")
}
