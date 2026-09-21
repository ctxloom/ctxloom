package coord

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResume_OneMessageOneLaunch pins the at-least-once seam between a
// resumed harp and its mail: one queued message must produce exactly one
// launch. A second launch for the same message finds in/ already consumed,
// sits idle having been told nothing, and the harp never reaches Ended — an
// idle run nobody notices. TestRetention_BoundsFoldGrowthAcrossResumes
// exercised the same loop and could only time out on that idle run; this
// test names the extra launch on the iteration it happens, with what each
// engine was handed.
func TestResume_OneMessageOneLaunch(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(
		map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *scriptedChat { return &scriptedChat{EndAfterTurns: 1} }, // ends its run after each turn
	)
	teeHome(t)
	c, err := New(Options{
		ProjectDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		Spawner:      sp,
		EndedRunTail: 1,
		OwnerHarp:    ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"the engine exits after its turn, ending the run")
	require.Equal(t, 1, sp.chatCount())

	const resumes = 6
	for i := 1; i <= resumes; i++ {
		_, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, fmt.Sprintf("turn %d", i), nil, "")
		require.NoError(t, err)
		awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
		require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp))
		cancel()
		require.Eventuallyf(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
			"resume %d never ended: %s", i, launchDiagnostic(c, sp, out.Harp))
		require.Equalf(t, 1+i, sp.chatCount(), "resume %d: one message, one launch — %s", i, launchDiagnostic(c, sp, out.Harp))
	}
}

// launchDiagnostic renders every engine the fake spawned and the turns each
// one was handed, plus the harp's pending mail count — the shape of a double
// launch is one engine with texts and one with none.
func launchDiagnostic(c *Coordinator, sp *fakeSpawner, harp string) string {
	s := fmt.Sprintf("pending=%d launches=%d", c.pendingCount(harp), sp.chatCount())
	for i := 0; i < sp.chatCount(); i++ {
		s += fmt.Sprintf(" chat%d=%v", i, sp.chat(i).RecordedTexts())
	}
	return s
}

// TestResumeChild_StaleAttemptStandsDownWhenTheHarpHasMovedOn pins the
// residual behind the double launch: terminateRun's leftover-mail tail arms
// a resume for the run that just ended, and nextRelaunch can charge that
// attempt a backoff. If the sleeper wakes after an explicit send has already
// resumed the harp AND that newer run has ended too, "the harp is Ended" is
// true again — but the mail the attempt was armed for was answered by the
// run in between. Such an attempt launches an engine that is handed nothing
// and idles forever. A resume is therefore armed FOR a run: when it wakes,
// that run must still be the harp's current one, or the attempt stands down.
func TestResumeChild_StaleAttemptStandsDownWhenTheHarpHasMovedOn(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(
		map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *scriptedChat { return &scriptedChat{EndAfterTurns: 1} },
	)
	teeHome(t)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	first := currentRunID(c, out.Harp)

	// An explicit send resumes the harp; that run answers and ends.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "turn", nil, "")
	require.NoError(t, err)
	awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp))
	cancel()
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	require.Equal(t, 2, sp.chatCount())
	second := currentRunID(c, out.Harp)
	require.NotEqual(t, first, second)

	// The sleeper armed for the FIRST run wakes now: the harp is Ended, but
	// it is a different run that ended. Nothing is queued for it to deliver.
	c.resumeChild(out.Harp, first, c.armLaunch(out.Harp), 0)
	assert.Equal(t, 2, sp.chatCount(), "an attempt armed for a run that is no longer current must not launch")
	assert.Equal(t, second, currentRunID(c, out.Harp))

	// Control: the same call armed for the run that IS current launches —
	// so what the stale attempt hit was the generation check, not a gate
	// that refuses every resume.
	c.resumeChild(out.Harp, second, c.armLaunch(out.Harp), 0)
	assert.Equal(t, 3, sp.chatCount())
}
