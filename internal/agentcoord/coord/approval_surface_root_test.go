package coord

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// WHO may answer an ActionSurfaceToHuman rung.
//
// The rung's whole meaning is "a human decides this", and the only identity a
// human answers as is the ROOT SESSION of the delegation tree. The coordinator
// used to authorize rec.ParentHarp instead — indistinguishable from the root
// at depth 1, and the INTERMEDIATE AGENT at depth 2. That is the shape these
// tests exist to keep out: they run a GRANDCHILD, because a depth-1 child
// cannot tell the two harps apart and so cannot fail either way.

// parkedLongEnough is the rung timeout these tests give the park. It is
// deliberately far longer than conformanceWait: the timeout firing is not
// this file's subject (surface_to_human_test.go's TestApproval_
// SurfaceToHumanTimeout owns that), and every assertion here happens WHILE
// the approval is parked — three refusals, each re-reading the audit journal
// off disk. Sizing that window at the ordinary conformance wait would make
// these tests fail under nothing worse than a loaded box.
const parkedLongEnough = 90 * time.Second

// surfaceGrandchildSpawner is relayGrandchildSpawner's sibling with a
// SURFACE_TO_HUMAN ladder: agent "worker" spawned twice, the second spawn (the
// grandchild) asking for permission, so exactly one approval walks the ladder
// and there is no ambiguity about whose decision is whose.
func surfaceGrandchildSpawner(timeout time.Duration, permReq *agent.PermissionRequest) *fakeSpawner {
	ladder := Ladder{{Action: ActionSurfaceToHuman, Role: ParentAddress, Timeout: timeout}}
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "plan", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
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

// awaitParkedApproval waits for the one human-surfaced approval this file's
// fixture produces and returns it.
func awaitParkedApproval(t *testing.T, parked <-chan PendingApproval) PendingApproval {
	t.Helper()
	select {
	case p := <-parked:
		return p
	case <-time.After(conformanceWait):
		t.Fatal("OnPendingApproval never fired for a parked human-surfaced approval")
		return PendingApproval{}
	}
}

func approvalDecisionJSON(t *testing.T, decision string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"decision": decision, "note": "reviewed by a human"})
	require.NoError(t, err)
	return raw
}

// TestApproval_SurfaceToHumanAtDepthTwo_OnlyTheRootSessionMayAnswer is the
// NEGATIVE half and the load-bearing one. A grandchild (depth 2) parks a
// surface_to_human rung; every identity in the tree EXCEPT the root session
// is refused, out loud, and the approval survives each refusal still
// outstanding. Only then does the root answer and reach the engine.
//
// A test that only proved the root can answer would be satisfied by deleting
// the targetHarp gate outright. The refusals are what make it a test of
// authorization rather than of plumbing.
func TestApproval_SurfaceToHumanAtDepthTwo_OnlyTheRootSessionMayAnswer(t *testing.T) {
	resetStrictness(t)
	sp := surfaceGrandchildSpawner(parkedLongEnough, commandExecRequest("perm-1"))
	c := newTestCoordinatorDepthCap(t, sp, nil, relayGenerationDepth)

	parked := make(chan PendingApproval, 1)
	c.OnPendingApproval(func(p PendingApproval, isParked bool) {
		if isParked {
			select {
			case parked <- p:
			default:
			}
		}
	})

	middle, grandchild, _ := spawnRelayGenerations(t, c, sp)
	require.NotEqual(t, ownerIdentity().Harp, middle.Harp, "fixture: the intermediate agent must not BE the root session")

	p := awaitParkedApproval(t, parked)
	assert.Equal(t, grandchild.Harp, p.Harp, "the parked approval names the GRANDCHILD that asked")

	// --- refusals ---
	for _, tc := range []struct {
		name   string
		caller Identity
	}{
		{
			// THE DEFECT. The intermediate agent is the asking run's parent,
			// and used to be the authorized answerer of a rung addressed to a
			// human — an agent deciding a question reserved for a person.
			name:   "the intermediate agent (the asking run's parent)",
			caller: Identity{Harp: middle.Harp, RunID: middle.RunID, Depth: 1},
		},
		{
			// Self-approval: whatever the lineage walk does, it must never
			// land on the asking run itself.
			name:   "the asking grandchild itself",
			caller: Identity{Harp: grandchild.Harp, RunID: grandchild.RunID, Depth: 2},
		},
		{
			name:   "an unrelated session guessing the correlation",
			caller: Identity{Harp: "some-other-session", Depth: 0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.AgentSend(tc.caller, grandchild.Harp, "", "reviewed", approvalDecisionJSON(t, "DECISION_ACCEPT"), p.MessageID)
			require.Error(t, err, "an answer from the wrong identity must be REFUSED, not accepted and not quietly demoted to ordinary mail")
			assert.Contains(t, err.Error(), "not authorized to answer that approval", "the refusal must say why")

			// The refusal is a RECORD, not just a return value.
			var refused bool
			for _, e := range readAuditKind(t, c, "approval_reply") {
				if e.Detail["in_reply_to"] == p.MessageID && e.Detail["resolution"] == "refused" && e.Actor == tc.caller.Harp {
					refused = true
				}
			}
			assert.True(t, refused, "an unauthorized answer must leave an audit fact naming who tried")

			// AND it consumed nothing: the approval is still answerable.
			listed := c.PendingApprovals()
			require.Len(t, listed, 1, "a refused answer must leave the approval outstanding")
			assert.Equal(t, p.MessageID, listed[0].MessageID)
			assert.Empty(t, sp.chat(1).recordedAnswers(), "no decision may reach the child's engine from an unauthorized answer")
		})
	}

	// --- the root session, and only it, resolves it ---
	disp, err := c.AgentSend(ownerIdentity(), grandchild.Harp, "", "reviewed", approvalDecisionJSON(t, "DECISION_ACCEPT"), p.MessageID)
	require.NoError(t, err, "the ROOT session is the identity a human answers as; it must be able to answer a rung surfaced to a human")
	assert.Contains(t, disp, "DECISION_ACCEPT")

	require.Eventually(t, func() bool {
		sc := sp.chat(1)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the grandchild's engine must receive the human's decision")
	assert.Equal(t, "allow-1", sp.chat(1).recordedAnswers()[0].OptionID)
	assert.Empty(t, c.PendingApprovals(), "an answered approval must leave the pending set")
}

// TestApproval_RelayToRoleAtDepthTwo_StillTargetsTheDirectParent is the
// counterweight: routing surface_to_human at the ROOT must not drag
// relay_to_role along with it. A relay addresses the asking run's PARENT ROLE
// by definition — the agent asked to decide — so at depth 2 the intermediate
// agent answers it and the root may not.
func TestApproval_RelayToRoleAtDepthTwo_StillTargetsTheDirectParent(t *testing.T) {
	resetStrictness(t)
	sp := relayGrandchildSpawner(parkedLongEnough, commandExecRequest("perm-1"))
	c := newTestCoordinatorDepthCap(t, sp, nil, relayGenerationDepth)

	middle, grandchild, _ := spawnRelayGenerations(t, c, sp)

	var askID string
	require.Eventually(t, func() bool {
		askID = pendingApprovalID(c, middle.Harp)
		return askID != ""
	}, conformanceWait, 10*time.Millisecond, "the relay never reached the intermediate agent's mailbox")

	// The ROOT is not this rung's answerer — a relay is addressed to a role.
	_, err := c.AgentSend(ownerIdentity(), grandchild.Harp, "", "reviewed", approvalDecisionJSON(t, "DECISION_ACCEPT"), askID)
	require.Error(t, err, "a relay_to_role rung is answered by the addressed ROLE, not by the root session")
	assert.Contains(t, err.Error(), "not authorized to answer that approval")
	assert.Empty(t, sp.chat(1).recordedAnswers(), "the root's refused answer must not reach the engine")

	// The addressed role does answer it.
	_, err = c.AgentSend(Identity{Harp: middle.Harp, RunID: middle.RunID, Depth: 1}, ParentAddress, "", "reviewed", approvalDecisionJSON(t, "DECISION_ACCEPT"), askID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(1)
		return sc != nil && len(sc.recordedAnswers()) == 1
	}, conformanceWait, 10*time.Millisecond, "the addressed role's decision must reach the asking child")
	assert.Equal(t, "allow-1", sp.chat(1).recordedAnswers()[0].OptionID)
}

// journalRun writes one runEnqueued straight into the runs journal — enough
// lineage for rootSessionHarp to climb, without spawning anything.
func journalRun(t *testing.T, c *Coordinator, runID, harp, parentHarp string, depth int) {
	t.Helper()
	require.NoError(t, c.runs.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factRunEnqueued, time.Now(), runEnqueued{
			RunID: runID, Harp: harp, Agent: "worker", ParentHarp: parentHarp,
			CredHash: runID + "-cred", Depth: depth,
		})}, nil
	}))
}

// TestRootSessionHarp_FailsClosedRatherThanNamingAnAgent covers the lineage
// climb's edges directly, including the ones no live topology reaches today.
// Each failure case asserts an ERROR, never a nearer harp: the caller treats
// an error as "park nothing", and the alternative — guessing an answerer —
// is the whole defect this function replaced.
func TestRootSessionHarp_FailsClosedRatherThanNamingAnAgent(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	// A plain owner session holds a credential and no run of its own.
	journalRun(t, c, "run-mid", "mid", "owner-session", 1)
	journalRun(t, c, "run-gc", "gc", "mid", 2)
	// A container top-level run journals itself as its own parent.
	journalRun(t, c, "run-owned", "owned", "owned", 0)
	journalRun(t, c, "run-mid2", "mid2", "owned", 1)
	journalRun(t, c, "run-gc2", "gc2", "mid2", 2)
	// Corrupt lineage: a delegated run at the top of the climb.
	journalRun(t, c, "run-orphan", "orphan", "", 2)
	journalRun(t, c, "run-below-orphan", "below-orphan", "orphan", 3)
	// A cycle among distinct harps.
	journalRun(t, c, "run-cyc-a", "cyc-a", "cyc-b", 1)
	journalRun(t, c, "run-cyc-b", "cyc-b", "cyc-a", 1)
	journalRun(t, c, "run-cyc-c", "cyc-c", "cyc-a", 2)

	for _, tc := range []struct {
		name    string
		rec     RunRecord
		want    string
		wantErr string
	}{
		{
			name: "a depth-2 child climbs past its parent to the owner session",
			rec:  RunRecord{RunID: "run-gc", Harp: "gc", ParentHarp: "mid"},
			want: "owner-session",
		},
		{
			name: "the climb stops at a container top-level run, which parents itself",
			rec:  RunRecord{RunID: "run-gc2", Harp: "gc2", ParentHarp: "mid2"},
			want: "owned",
		},
		{
			name:    "a run with no parent has no root to climb to",
			rec:     RunRecord{RunID: "run-x", Harp: "x"},
			wantErr: "records no parent",
		},
		{
			name:    "a run that parents itself is not its own approver",
			rec:     RunRecord{RunID: "run-x", Harp: "x", ParentHarp: "x"},
			wantErr: "records no parent",
		},
		{
			name:    "a climb topping out at a delegated run is refused, not accepted",
			rec:     RunRecord{RunID: "run-below-orphan", Harp: "below-orphan", ParentHarp: "orphan"},
			wantErr: "itself a delegated run",
		},
		{
			name:    "a lineage cycle terminates as an error, not as a harp",
			rec:     RunRecord{RunID: "run-cyc-c", Harp: "cyc-c", ParentHarp: "cyc-a"},
			wantErr: "did not reach a root session",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.rootSessionHarp(tc.rec)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Empty(t, got, "a failed climb must name NO answerer at all")
				assert.NotEqual(t, tc.rec.Harp, got, "the asking run must never be its own approval's answerer")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
