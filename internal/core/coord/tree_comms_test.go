package coord

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The delegation topology is a TREE: every hop addresses its own direct
// parent or its own direct children, a nested parent is a real correspondent,
// and the root is a tree node like any other (it addresses and stops only its
// own children). These tests pin each edge of that rule on a three-level tree:
// the root (the session owner), a depth-1 parent, and its depth-2 child.

// tree is a root -> parent -> grandchild delegation tree on one coordinator.
type tree struct {
	c          *Coordinator
	sp         *fakeSpawner
	parent     Identity
	grandchild Identity
	// release opens the grandchild's held turn; nil when it is not held.
	release func()
}

// spawnTree builds the tree with the depth cap raised to 2. The parent is
// engine 0 and the grandchild engine 1 (spawn order). When holdGrandchild is
// set the grandchild's turns are GATED, so its first turn never reaches a
// boundary on its own: nothing it would bridge to the parent can give the
// parent an extra turn the test did not ask for.
func spawnTree(t *testing.T, holdGrandchild bool) tree {
	t.Helper()
	resetStrictness(t)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	var mu sync.Mutex
	engines := 0
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, func() *scriptedChat {
		mu.Lock()
		defer mu.Unlock()
		engines++
		if holdGrandchild && engines == 2 {
			return &scriptedChat{Gate: gate}
		}
		return &scriptedChat{}
	})
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	// Registered after the coordinator, so it runs FIRST: the held turn is
	// released before Close waits on the grandchild's runner.
	t.Cleanup(release)

	pOut, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "coordinate a subtask", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.chat(0)
		return e != nil && len(e.RecordedTexts()) == 1 && nativeSession(c, pOut.Harp) != ""
	}, conformanceWait, 10*time.Millisecond, "the parent's first turn never reached its engine")
	parent := Identity{Harp: pOut.Harp, RunID: pOut.RunID, Depth: 1}

	gOut, err := c.AgentRun(context.Background(), parent, "worker", "do the subtask", "", "")
	require.NoError(t, err)
	awaitChatText(t, sp, 1, "do the subtask")
	out := tree{c: c, sp: sp, parent: parent, grandchild: Identity{Harp: gOut.Harp, RunID: gOut.RunID, Depth: 2}}
	if holdGrandchild {
		out.release = release
	}
	return out
}

// endParent ends the parent's run through terminateRun, the transition itself,
// so the terminal — and its own leftover-mail tail, which finds an empty
// mailbox — is whole before the test acts. The parent's exited notice is
// drained from the owner's inbox.
func (tr tree) endParent(t *testing.T) {
	t.Helper()
	tr.c.terminateRun(tr.parent.RunID, CauseRunnerExit, "engine exited")
	require.Equal(t, StateEnded, rosterState(tr.c, tr.parent.Harp))
	require.Len(t, recvKind(t, tr.c, KindExited, time.Second), 1)
}

// awaitParentResumedWith waits for the parent's harp to come back as a fresh
// engine (index 2) whose turns carry want.
func (tr tree) awaitParentResumedWith(t *testing.T, want string) {
	t.Helper()
	require.Eventually(t, func() bool { return tr.sp.spawnCount() == 3 }, conformanceWait, 10*time.Millisecond,
		"the ended parent was never resumed: mail from its child is stranded in its spool")
	awaitChatText(t, tr.sp, 2, want)
}

// TestTree_MidTreeParentSendsToItsOwnChild: a depth-1 parent has a DOWNWARD
// edge. Its agent_send to its own depth-2 child is delivered as that child's
// next turn, not refused as a hub-and-spoke violation.
func TestTree_MidTreeParentSendsToItsOwnChild(t *testing.T) {
	tr := spawnTree(t, false)

	_, err := tr.c.AgentSend(tr.parent, tr.grandchild.Harp, KindMessage, "narrow it to the parser", nil, "")
	require.NoError(t, err)
	awaitChatText(t, tr.sp, 1, "narrow it to the parser")
}

// TestTree_GrandchildMailResumesItsEndedParent: child-origin mail to a parent
// whose run has ENDED resumes that parent with the mail, rather than waiting
// on a doorbell no runner will ever hear.
func TestTree_GrandchildMailResumesItsEndedParent(t *testing.T) {
	tr := spawnTree(t, true)
	tr.endParent(t)

	_, err := tr.c.AgentSend(tr.grandchild, ParentAddress, KindResult, "the parser is fixed", nil, "")
	require.NoError(t, err)
	tr.awaitParentResumedWith(t, "the parser is fixed")
}

// TestTree_GrandchildFinalReportLandsWithItsEndedParent: agent_report lands
// with the DIRECT parent (ruling a), including a parent whose run has ended —
// the report resumes it. The root does not receive it as mail.
func TestTree_GrandchildFinalReportLandsWithItsEndedParent(t *testing.T) {
	tr := spawnTree(t, true)
	tr.endParent(t)

	require.NoError(t, tr.c.Report(context.Background(), tr.grandchild, ReportRequest{Scope: "final", Body: "deliverable: parser fixed"}))
	tr.awaitParentResumedWith(t, "deliverable: parser fixed")
	assert.Empty(t, recvBody(t, tr.c, "deliverable: parser fixed", 100*time.Millisecond),
		"a grandchild's report is addressed to its parent, not the root")
}

// TestTree_RootAddressesOnlyItsOwnChildren: the root is a tree node (ruling
// c). It may not message its grandchild past the grandchild's parent.
func TestTree_RootAddressesOnlyItsOwnChildren(t *testing.T) {
	tr := spawnTree(t, false)

	_, err := tr.c.AgentSend(ownerIdentity(), tr.grandchild.Harp, KindMessage, "skip your parent", nil, "")
	require.ErrorIs(t, err, ErrNotAChild)

	_, err = tr.c.AgentSend(ownerIdentity(), tr.parent.Harp, KindMessage, "for the parent", nil, "")
	require.NoError(t, err, "the root still addresses its own child")
}

// TestTree_RootStopsOnlyItsOwnChildren: the root may not stop its grandchild
// (ruling c), and the grandchild's own parent may.
func TestTree_RootStopsOnlyItsOwnChildren(t *testing.T) {
	tr := spawnTree(t, false)

	_, err := tr.c.AgentStop(ownerIdentity(), tr.grandchild.Harp, "", 0)
	require.ErrorIs(t, err, ErrNotAChild)
	assert.NotEqual(t, StateEnded, rosterState(tr.c, tr.grandchild.Harp), "a refused stop must not end the run")
}

// TestTree_MidTreeParentStopsItsOwnChild: agent_stop on the wire (run_id
// form) from a depth-1 parent stops its own depth-2 child.
func TestTree_MidTreeParentStopsItsOwnChild(t *testing.T) {
	tr := spawnTree(t, false)

	reply := tr.c.serveStopRun(context.Background(), tr.parent, StopRun{RunID: tr.grandchild.RunID, Grace: time.Millisecond})
	require.NoError(t, reply.Err)
	assert.Equal(t, StateEnded, rosterState(tr.c, tr.grandchild.Harp))
}

// rosterHarps lists the harps a roster reply carries.
func rosterHarps(t *testing.T, reply AgentReply) []string {
	t.Helper()
	require.NoError(t, reply.Err)
	snap, ok := reply.Result.(RunsSnapshot)
	require.True(t, ok, "roster answers a RunsSnapshot, got %T", reply.Result)
	var out []string
	for _, r := range snap.Runs {
		out = append(out, r.Agent.AgentID)
	}
	return out
}

// TestTree_RosterIsScopedToTheCallersChildren: a mid-tree parent's roster
// lists its own children and not the tree; a leaf's lists nothing; the root
// keeps read-only visibility of every descendant.
func TestTree_RosterIsScopedToTheCallersChildren(t *testing.T) {
	tr := spawnTree(t, false)
	req := RosterRequest{IncludeTerminal: true}

	assert.Equal(t, []string{tr.grandchild.Harp}, rosterHarps(t, tr.c.serveRoster(tr.parent, req)))
	assert.Empty(t, rosterHarps(t, tr.c.serveRoster(tr.grandchild, req)))
	assert.ElementsMatch(t, []string{tr.parent.Harp, tr.grandchild.Harp}, rosterHarps(t, tr.c.serveRoster(ownerIdentity(), req)))
}
