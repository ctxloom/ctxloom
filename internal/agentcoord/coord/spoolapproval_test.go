package coord

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the APPROVAL LADDER's relay leg under the cutover: the hop between
// the coordinator and a remote answerer changes CARRIER (a file, correlated by
// in_reply_to) and nothing else. The ladder walk, the rung timeouts, the
// ACCEPT_FOR_SESSION cache, the audit journal and the bottom-is-a-CANCEL rule
// are untouched, and this file's whole point is to prove that by comparing the
// two carriers' audit trails against each other.
//
// The topology is a GRANDCHILD, and it has to be: relayApproval addresses the
// asking run's PARENT, and the only parent that is itself a spool-delivered run
// is one that is a tracked child in its own right. A child of the session owner
// relays to the owner's in-process mailbox, which is not on the file plane at
// all (spoolDeliverTo's second condition).

// relayGrandchildSpawner builds the two-generation fixture: agent "worker"
// under an explicit relay-to-parent ladder, spawned twice. The FIRST spawn (the
// middle generation) gets a plain engine; the SECOND (the grandchild) gets one
// that asks for permission, so exactly one approval walks the ladder and the
// audit trail is unambiguous about whose it is.
func relayGrandchildSpawner(timeout time.Duration, permReq *agent.PermissionRequest) *fakeSpawner {
	ladder := Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: timeout}}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
	}, nil)
	var mu sync.Mutex
	spawned := 0
	sp.nextChat = func() *scriptedChat {
		mu.Lock()
		defer mu.Unlock()
		spawned++
		if spawned == 1 {
			return &scriptedChat{}
		}
		return &scriptedChat{permission: permReq}
	}
	sp.engineCaps = RunnerCapabilities(true)
	return sp
}

// relayGenerationDepth is the delegation depth these tests need: a grandchild
// is depth 2, and the package default cap is 1. It is raised for BOTH carriers
// identically — a comparison whose two halves ran under different limits would
// not be a comparison.
const relayGenerationDepth = 2

// newDeepCutoverCoordinator is newCutoverCoordinator with the depth cap raised
// far enough for a grandchild, at the production sweep cadence.
func newDeepCutoverCoordinator(t *testing.T, sp Spawner) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir:    t.TempDir(),
		StateDir:      t.TempDir(),
		Spawner:       sp,
		SpoolDelivery: true,
		Depth:         relayGenerationDepth,
	})
	require.NoError(t, err, "new cutover coordinator")
	require.NoError(t, c.Serve(), "serve cutover coordinator")
	t.Cleanup(c.Close)
	return c
}

// spawnRelayGenerations spawns the middle generation and then, AS THAT CHILD,
// the grandchild whose approval walks the ladder. It returns both runs and the
// middle generation's runner Home — the identity that must answer.
func spawnRelayGenerations(t *testing.T, c *Coordinator, sp *fakeSpawner) (middle *RunOutcome, grandchild *RunOutcome, middleHome *Home) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	middle, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "supervise", "", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(ctx, middle.Harp), "the middle generation never came up")
	require.Eventually(t, func() bool { return sp.engineHome(0) != nil }, conformanceWait, 10*time.Millisecond)
	middleHome = sp.engineHome(0)

	// Spawned BY the middle generation, so its parent — and therefore the
	// ladder's relay target — is a tracked run rather than the session owner.
	grandchild, err = c.AgentRun(context.Background(),
		Identity{Harp: middle.Harp, RunID: middle.RunID, Depth: 1}, "worker", "run a command", "", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(ctx, grandchild.Harp), "the grandchild never came up")
	return middle, grandchild, middleHome
}

// approvalAuditShape is one audit entry reduced to what the LADDER decided,
// with the run-specific identifiers dropped.
//
// Dropping them is what makes the comparison possible at all (two runs mint
// different run ids and different harps), and keeping everything else is what
// makes it meaningful: the rung, the action, the role, the resolution and the
// decision are the whole content of "which rung answered and what did it say".
type approvalAuditShape struct {
	Kind       string `json:"kind"`
	Rung       string `json:"rung"`
	Action     string `json:"action"`
	Role       string `json:"role"`
	Resolution string `json:"resolution"`
	Decision   string `json:"decision"`
}

func approvalAuditShapes(t *testing.T, c *Coordinator) []approvalAuditShape {
	t.Helper()
	entries := readApprovalAudit(t, c)
	out := make([]approvalAuditShape, 0, len(entries))
	for _, e := range entries {
		out = append(out, approvalAuditShape{
			Kind:       e.Detail["kind"],
			Rung:       e.Detail["rung"],
			Action:     e.Detail["action"],
			Role:       e.Detail["role"],
			Resolution: e.Detail["resolution"],
			Decision:   e.Detail["decision"],
		})
	}
	return out
}

// relayedApprovalID finds the correlation id of the approval_request relayed
// into harp's spool — the id the answer quotes.
//
// It reads it out of the FILE, which is the point of the test: under the
// cutover the relay is a spool file, and if it were not there would be nothing
// here to read.
func relayedApprovalID(t *testing.T, harp string) (string, spool.Entry) {
	t.Helper()
	var found spool.Entry
	require.Eventually(t, func() bool {
		for _, dir := range []spool.Dir{spool.DirIn, spool.DirInConsumed} {
			for _, e := range spoolEntries(t, harp, dir) {
				if e.Message.Kind == KindApprovalRequest {
					found = e
					return true
				}
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the relayed approval never appeared as a file in %s's spool", harp)
	require.NotEmpty(t, found.Message.OriginID,
		"the relay's correlation id must ride the file as origin_id: the coordinator registered the waiter under it before the file existed")
	return found.Message.OriginID, found
}

// TestSpoolApproval_RelayRidesFilesAndAuditsIdentically is the plane-4 happy
// path AND the parity assertion in one: the relay leg travels as a file, the
// decision comes back as a correlated file, the child gets the option the
// answerer chose — and the coordinator's per-rung audit trail is
// INDISTINGUISHABLE from the mailbox carrier's.
//
// The parity is the load-bearing half. An approval system has one thing it must
// never do, which is report a decision nobody made; the audit journal is where
// that would show, and a carrier swap that quietly changed what got journaled
// would be exactly the defect this project has already fixed three times.
func TestSpoolApproval_RelayRidesFilesAndAuditsIdentically(t *testing.T) {
	resetStrictness(t)

	// --- the file carrier ---
	teeHome(t)
	sp := relayGrandchildSpawner(conformanceWait, commandExecRequest("perm-1"))
	c := newDeepCutoverCoordinator(t, sp)
	middle, grandchild, middleHome := spawnRelayGenerations(t, c, sp)
	require.True(t, middleHome.SpoolDeliveryEnabled(), "the answering generation must be on the file plane")

	askID, entry := relayedApprovalID(t, middle.Harp)
	require.NotNil(t, entry.Message)
	assert.Contains(t, string(mustMarshal(t, entry.Message.Structured)), "APPROVAL_KIND_COMMAND_EXECUTION",
		"the request's proto projection must ride the file, or the answerer is deciding blind")
	assert.Contains(t, string(mustMarshal(t, entry.Message.Structured)), "rm -rf /tmp/scratch")

	// The answerer replies with an ApprovalDecision, correlated by in_reply_to
	// — an ordinary agent_send, which under the cutover is a local write into
	// its own out/ spool.
	decisionStruct := mustStruct(t, map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
	resp, err := middleHome.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "reviewed", InReplyTo: askID, Structured: decisionStruct,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the decision must be accepted: %s", resp.GetStatus().GetMessage())

	// The asking grandchild's engine gets the option the answerer chose.
	require.Eventually(t, func() bool {
		sc := sp.chat(1)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the asking child must receive the decision")
	assert.Equal(t, "allow-1", sp.chat(1).recordedAnswers()[0].OptionID)
	assert.NotEmpty(t, grandchild.Harp)

	fileShapes := approvalAuditShapes(t, c)
	require.NotEmpty(t, fileShapes, "the audit trail must not be empty, or the comparison below proves nothing")

	// --- the mailbox carrier, same ladder, same decision ---
	teeHome(t)
	mailSp := relayGrandchildSpawner(conformanceWait, commandExecRequest("perm-1"))
	mailC := newTestCoordinatorDepthCap(t, mailSp, nil, relayGenerationDepth)
	require.False(t, mailC.SpoolDeliveryEnabled())
	mailMiddle, _, mailMiddleHome := spawnRelayGenerations(t, mailC, mailSp)

	var mailAskID string
	require.Eventually(t, func() bool {
		mailAskID = pendingApprovalID(mailC, mailMiddle.Harp)
		return mailAskID != ""
	}, conformanceWait, 10*time.Millisecond, "the relay never reached the answerer's mailbox")
	resp, err = mailMiddleHome.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "reviewed", InReplyTo: mailAskID, Structured: decisionStruct,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "%s", resp.GetStatus().GetMessage())
	require.Eventually(t, func() bool {
		sc := mailSp.chat(1)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the mailbox carrier must resolve the same approval")

	mailShapes := approvalAuditShapes(t, mailC)
	require.NotEmpty(t, mailShapes)
	assert.Equal(t, string(mustMarshal(t, mailShapes)), string(mustMarshal(t, fileShapes)),
		"the carrier changed; what the ladder decided and journaled must not have")
}

// TestSpoolApproval_CoordinatorRefusalIsACancelNotAUserDecision pins
// wiry-judge across the carrier swap: when no rung resolves, the bottom is a
// CANCEL — nobody decided, so nobody refused.
//
// A DECLINE here would not stay an abstraction: it selects a reject_once option
// at the runner, which the engine records in its own durable transcript as the
// user having refused. That is ctxloom's own timeout wearing the operator's
// face, and the file carrier must not be where it creeps back in.
func TestSpoolApproval_CoordinatorRefusalIsACancelNotAUserDecision(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	// A rung that times out before anyone answers, and nobody answers.
	sp := relayGrandchildSpawner(150*time.Millisecond, commandExecRequest("perm-1"))
	c := newDeepCutoverCoordinator(t, sp)
	middle, _, _ := spawnRelayGenerations(t, c, sp)

	// The relay really did ride a file — otherwise this test would pass on the
	// mailbox path and prove nothing about the cutover.
	_, entry := relayedApprovalID(t, middle.Harp)
	require.Equal(t, KindApprovalRequest, entry.Message.Kind)

	require.Eventually(t, func() bool {
		for _, e := range approvalAuditShapes(t, c) {
			if e.Rung == "bottom" {
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the ladder must bottom out once its only rung times out")

	shapes := approvalAuditShapes(t, c)
	require.NotEmpty(t, shapes)
	var sawTimeout, sawBottom bool
	for _, e := range shapes {
		switch e.Rung {
		case "bottom":
			sawBottom = true
			assert.Equal(t, "cancelled", e.Resolution,
				"a rung that nobody answered is a CANCEL: reporting a DECLINE would journal ctxloom's own timeout as the operator's refusal")
			assert.Empty(t, e.Decision, "the bottom is not a decision anyone made")
		default:
			if e.Resolution == "timed_out" {
				sawTimeout = true
				assert.Equal(t, string(ActionRelayToRole), e.Action)
			}
		}
		assert.NotEqual(t, "granted", e.Resolution, "nothing granted this: no rung resolved at all")
	}
	assert.True(t, sawTimeout, "the timed-out rung must be journaled as timed out, not as a decision")
	assert.True(t, sawBottom)
}

// pendingApprovalID drains the ANSWERER's mailbox for a relayed
// approval_request and returns its correlation id — the mailbox carrier's twin
// of relayedApprovalID.
func pendingApprovalID(c *Coordinator, harp string) string {
	msgs, err := c.recvMail(context.Background(), harp, 10*time.Millisecond)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Kind == KindApprovalRequest {
			return m.ID
		}
	}
	return ""
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}
