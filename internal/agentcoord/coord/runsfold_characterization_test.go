package coord

import (
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunsFold_Apply_EveryArm characterizes runsFold.apply arm by arm, so the
// split of that switch into per-fact handlers (CCN 26 against the
// project's CCN-10 gate) is provably behaviour-preserving. It is deliberately
// exhaustive about the arms' EDGES — the guards are where a mechanical split
// loses something: ignoring a fact for an unknown run, refusing to reopen an
// ended run, credential revocation riding the terminal fact, and the reap's
// deliberate asymmetry (records go, byHarp stays).
func TestRunsFold_Apply_EveryArm(t *testing.T) {
	base := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	f := newRunsFold()

	// A session credential also stamps the fold's project, which every later
	// run credential inherits.
	f.apply(factAt(factSessionCred, at(0), sessionCred{Harp: "owner", Project: "proj-x", CredHash: "owner-hash"}))
	id, ok := f.identityFor("owner-hash")
	require.True(t, ok)
	assert.Equal(t, Identity{Harp: "owner", Depth: 0, Project: "proj-x"}, id)

	// factRunEnqueued mints the record, the harp index, and the credential.
	f.apply(factAt(factRunEnqueued, at(1), runEnqueued{
		RunID: "r1", Harp: "kid", Agent: "worker", ParentHarp: "owner", ParentRunID: "r0",
		Runtime: agent.RuntimeContainerRootless, CredHash: "c1", Depth: 1, Prompt: "brief",
		Permission: "bypass", MCPServers: []string{"ctxloom"},
		Ladder: []ladderRungFact{{Action: "decline"}},
	}))
	r := f.run("r1")
	require.NotNil(t, r)
	assert.Equal(t, StateQueued, r.State)
	assert.Equal(t, "kid", r.Harp)
	assert.Equal(t, "worker", r.Agent)
	assert.Equal(t, "owner", r.ParentHarp)
	assert.Equal(t, "r0", r.ParentRunID)
	assert.Equal(t, agent.RuntimeContainerRootless, r.Runtime)
	assert.Equal(t, 1, r.Depth)
	assert.Equal(t, "brief", r.Prompt)
	assert.Equal(t, "bypass", r.Permission)
	assert.Equal(t, []string{"ctxloom"}, r.MCPServers)
	assert.Len(t, r.Ladder, 1)
	assert.Equal(t, at(1), r.EnqueuedAt)
	assert.Equal(t, at(1), r.LastActivity)
	assert.Equal(t, r, f.currentRun("kid"))
	credID, ok := f.identityFor("c1")
	require.True(t, ok)
	assert.Equal(t, Identity{Harp: "kid", RunID: "r1", Depth: 1, Project: "proj-x"}, credID)

	// factRunState advances state and touches activity.
	f.apply(factAt(factRunState, at(2), runState{RunID: "r1", State: StateExecuting}))
	assert.Equal(t, StateExecuting, f.run("r1").State)
	assert.Equal(t, at(2), f.run("r1").LastActivity)
	assert.Equal(t, []string{"r1"}, f.activeRunsForCred("c1"))

	// A state fact for an unknown run is ignored, not a panic and not a
	// phantom record.
	f.apply(factAt(factRunState, at(3), runState{RunID: "nope", State: StateIdle}))
	assert.Nil(t, f.run("nope"))

	// factRunHarness / factRunResumable bind the resume pair.
	f.apply(factAt(factRunHarness, at(4), runHarness{RunID: "r1", HarnessSessionID: "sess-9"}))
	f.apply(factAt(factRunResumable, at(4), runResumable{RunID: "r1", Resumable: true}))
	assert.Equal(t, "sess-9", f.run("r1").HarnessSessionID)
	assert.True(t, f.run("r1").Resumable)

	// factRunEnded is terminal AND revokes the credential.
	f.apply(factAt(factRunEnded, at(5), runEnded{RunID: "r1", Cause: CauseStopped, Detail: "stopped by owner"}))
	r = f.run("r1")
	assert.True(t, r.Ended)
	assert.Equal(t, StateEnded, r.State)
	assert.Equal(t, CauseStopped, r.Cause)
	assert.Equal(t, "stopped by owner", r.Detail)
	_, ok = f.identityFor("c1")
	assert.False(t, ok, "the credential dies with the run")
	assert.Empty(t, f.activeRunsForCred("c1"))

	// An ended run does not reopen, and a second terminal does not overwrite
	// the first cause.
	f.apply(factAt(factRunState, at(6), runState{RunID: "r1", State: StateExecuting}))
	f.apply(factAt(factRunEnded, at(6), runEnded{RunID: "r1", Cause: CauseRunnerLoss}))
	assert.Equal(t, StateEnded, f.run("r1").State)
	assert.Equal(t, CauseStopped, f.run("r1").Cause)
	assert.Equal(t, at(5), f.run("r1").LastActivity)

	// A resume enqueues a NEW run for the same harp; byHarp follows it.
	f.apply(factAt(factRunEnqueued, at(7), runEnqueued{RunID: "r2", Harp: "kid", CredHash: "c2", Depth: 1, Resume: true}))
	assert.Equal(t, "r2", f.currentRun("kid").RunID)

	// factRunReaped drops the listed records but NEVER byHarp — the resume key
	// the index points at must survive.
	f.apply(factAt(factRunReaped, at(8), runReaped{RunIDs: []string{"r1"}}))
	assert.Nil(t, f.run("r1"))
	assert.Equal(t, "r2", f.currentRun("kid").RunID)

	// factSessionCredRevoked removes only the named credential.
	f.apply(factAt(factSessionCredRevoked, at(9), sessionCred{CredHash: "owner-hash"}))
	_, ok = f.identityFor("owner-hash")
	assert.False(t, ok)
	_, ok = f.identityFor("c2")
	assert.True(t, ok, "revoking one credential leaves the others")

	// An undecodable payload is skipped, and an unknown kind is ignored —
	// forward compatibility (an older binary replaying a newer journal).
	f.apply(Fact{Kind: factRunEnqueued, At: at(10), Data: []byte(`"not an object"`)})
	f.apply(Fact{Kind: "run.fromTheFuture", At: at(10), Data: []byte(`{}`)})
	assert.Len(t, f.runs, 1)
}
