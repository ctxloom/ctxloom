package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// harnessSessionID reads the CURRENT run's journaled HarnessSessionID for
// harp — the resume key recordHarnessSession writes (children.go:602),
// exposed here for the assertion below rather than reaching into the fold
// inline at every call site (mirrors resumeChild's own read).
func harnessSessionID(c *Coordinator, harp string) string {
	var id string
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			id = r.HarnessSessionID
		}
	})
	return id
}

// TestHandleChildEvent_CapturesLegacySessionID_AndThreadsOnResume proves: a
// LEGACY (non-viaStartRun) backend's native session
// id — a vendor-native conversation id is the production shape this
// fakeEngine's `sessionID` field stands in for — is now (a) CAPTURED off
// ev.Session by handleChildEvent instead of silently dropped, and (b)
// THREADED back into the backend on resume (Spawner.Launch's new
// resumeSessionID parameter) instead of falling back to the lossy
// ResumeContext transcript replay.
//
// Before this fix: handleChildEvent's switch had no `ev.Session != nil`
// case at all, so RunRecord.HarnessSessionID stayed "" forever for every
// legacy-path backend, and resumeChild's legacy tail unconditionally called
// c.spawner.ResumeContext (rendered-transcript replay) with no way to pass a
// resume id to Launch even if it HAD one — see the temporary revert below
// this test, which reproduces exactly that regression against the current
// tree.
func TestHandleChildEvent_CapturesLegacySessionID_AndThreadsOnResume(t *testing.T) {
	resetStrictness(t)
	const nativeID = "agy-conversation-abc123"
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{endAfterTurns: 1, sessionID: nativeID} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)

	// (a) CAPTURE: the native session id emitted on ev.Session made it into
	// the run journal — previously always "" for a legacy backend.
	require.Eventually(t, func() bool { return harnessSessionID(c, out.Harp) == nativeID },
		conformanceWait, 10*time.Millisecond, "the legacy backend's native session id was never captured")

	// Drain the synthesized terminal notice so the resume trigger below is
	// clean (mirrors TestAgentSend_ResumesEndedChild).
	_, _ = c.AgentRecv(context.Background(), ownerIdentity(), 20*time.Millisecond)

	disp, err := c.AgentSend(ownerIdentity(), out.Harp, "", "one more thing", nil, "")
	require.NoError(t, err)
	assert.Contains(t, disp, "resuming")

	// (b) THREAD ON RESUME: the resumed engine's Launch call carried the
	// captured native id as resumeSessionID, and the first turn was NOT
	// primed with a rendered-transcript replay (contextText empty — the
	// backend resumes its OWN session instead).
	require.Eventually(t, func() bool { return sp.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	resumed := sp.engine(1)
	require.NotNil(t, resumed)
	assert.Equal(t, nativeID, resumed.resumeSessionID(),
		"resumeChild must pass the captured native id to Spawner.Launch, not fall back to transcript replay")

	require.Eventually(t, func() bool { return len(resumed.recordedTexts()) == 1 }, conformanceWait, 10*time.Millisecond)
	resumedFirst := resumed.recordedTexts()[0]
	assert.Equal(t, frameCoordinatorDelivery(ownerIdentity().Harp, "", "one more thing"), resumedFirst,
		"a native-id resume carries only the mail's provenance frame — never a rendered-transcript replay — on the first turn")
}
