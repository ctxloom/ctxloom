package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A top-level run ending is not a delegated child dying. An owner-owned run
// journals its OWN harp as its parent (the self-loop owner_run.go names), so
// the child-death tail of terminateRun — mail the parent a terminal notice,
// then relaunch the harp if it has leftover mail — read it as a child of
// itself: it mailed the owner "you exited", saw that very notice as leftover
// mail, and tried to resume the owner's harp as a declared agent. Every plain
// `ctxloom run` ended with "agent resume <harp>: the named agent is not found".
func TestTerminateRun_OwnerRunIsNotAChildOfItself(t *testing.T) {
	// A resume attempt on the owner's harp fails to resolve it as an agent
	// and queues "could not be resumed" into the owner's own inbox, so the
	// inbox count holding still over the window IS "no resume was armed".
	// The retry backoff is shrunk so a second relaunch would land inside it.
	t.Setenv(EnvLaunchBackoffBase, "1ms")
	for _, tc := range []struct {
		name     string
		leftover bool // real mail reaches the owner after its run ended
		deliver  bool // an explicit delivery finds the owner's run ended
	}{
		{name: "the end queues no notice to itself"},
		{name: "leftover mail waits instead of relaunching", leftover: true},
		{name: "a delivery to the ended owner does not resume it", deliver: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			sp := newFakeSpawner(t, nil, nil)
			c := newTestCoordinator(t, sp, nil)
			ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
			defer cancel()

			const ownerHarp = "owner-harp"
			token, err := c.RegisterSessionOwner(ownerHarp)
			require.NoError(t, err)
			owner, ok := c.Identify(token)
			require.True(t, ok)

			starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
			out, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass"), false), starter, "do the thing")
			require.NoError(t, err)
			require.True(t, *started)

			c.terminateRun(out.RunID, CauseRunnerExit, "")
			require.Zero(t, c.pendingCount(ownerHarp),
				"the owner is not its own parent: its run ending queues it no terminal notice")

			if tc.leftover {
				_, err := c.queueMail(ownerHarp, ownerHarp, "message", "a turn the run never took")
				require.NoError(t, err)
				var rec RunRecord
				c.runs.View(func() { rec = *c.runsF.run(out.RunID) })
				c.relaunchForLeftoverMail(rec, CauseRunnerExit, "")
			}
			if tc.deliver {
				c.driveObserved(ownerHarp, StateEnded, out.RunID)
			}
			want := c.pendingCount(ownerHarp)
			assert.Never(t, func() bool { return c.pendingCount(ownerHarp) != want }, 300*time.Millisecond, 20*time.Millisecond,
				"a top-level run is not a declared agent: its end never arms a resume of its harp")
		})
	}
}

// TestServeStopRun_RefusesAnyRunNotTheCallersChild: agent_stop with a run_id
// stops only a delegated child of the caller. The caller's OWN run journals
// the caller's harp as its parent, and must not read as its child — the stop
// would close the session's runner mid-turn, under the very call that asked
// for it. A run id the coordinator does not know is refused the same way.
func TestServeStopRun_RefusesAnyRunNotTheCallersChild(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(t, nil, nil), nil)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	out, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass"), false), starter, "do the thing")
	require.NoError(t, err)
	require.True(t, *started)

	reply := c.serveStopRun(ctx, owner, StopRun{RunID: out.RunID, Reason: "stop myself"})
	require.ErrorIs(t, reply.Err, ErrNotAChild, "the caller's own run is not its child")
	var rec RunRecord
	c.runs.View(func() { rec = *c.runsF.run(out.RunID) })
	assert.NotEqual(t, CauseStopped, rec.Cause, "the refused stop stopped the caller's own run")

	reply = c.serveStopRun(ctx, owner, StopRun{RunID: "no-such-run", Reason: "stop it"})
	require.ErrorIs(t, reply.Err, ErrNotAChild, "an unknown run is no child of anyone's")
}

// relaunchForLeftoverMail's top-level guard is not redundant with
// resumeChild's refusal of a top-level record: that refusal comes only AFTER
// the tail has already spent relaunch budget (nextRelaunch) and started a
// tracked resume goroutine. The guard has to stop an owner run's leftover mail
// before any of that happens. The budget counter is advanced synchronously, so
// reading it right after the call settles the question without a timing window.
func TestRelaunchForLeftoverMail_TopLevelRunArmsNoRelaunch(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	out, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass"), false), starter, "do the thing")
	require.NoError(t, err)
	require.True(t, *started)
	c.terminateRun(out.RunID, CauseRunnerExit, "")

	_, err = c.queueMail(ownerHarp, ownerHarp, "message", "a turn the run never took")
	require.NoError(t, err)
	var rec RunRecord
	c.runs.View(func() { rec = *c.runsF.run(out.RunID) })
	require.True(t, rec.TopLevel(), "precondition: the owner's run is top-level")
	require.Positive(t, c.pendingCount(ownerHarp), "precondition: the owner has leftover mail")

	c.relaunchForLeftoverMail(rec, CauseRunnerExit, "")

	c.mu.Lock()
	st := c.launches[ownerHarp]
	c.mu.Unlock()
	if st != nil {
		assert.Zero(t, st.relaunches,
			"a top-level run's leftover mail spent relaunch budget: the tail armed a relaunch of the owner's harp")
	}
}

// TestChildOf_NoParentIsNobodysChild: a run that journals no parent (a
// plugin-hosted session's own run) is no caller's child, not even a caller
// that names no harp, to whom "ParentHarp == parent" alone would hand every
// such run. No production caller has an empty harp today (New refuses an
// empty owner, and the one harp-less identity, a consumer, is refused on the
// RunChannel before it can ask); this pins the guard that does not rely on it.
func TestChildOf_NoParentIsNobodysChild(t *testing.T) {
	assert.False(t, RunRecord{Harp: "plugin-harp"}.ChildOf(""))
	assert.False(t, RunRecord{Harp: "owner-harp", ParentHarp: "owner-harp"}.ChildOf("owner-harp"), "an owner run is not its own child")
	assert.True(t, RunRecord{Harp: "child-harp", ParentHarp: "owner-harp"}.ChildOf("owner-harp"))
	assert.False(t, RunRecord{Harp: "child-harp", ParentHarp: "owner-harp"}.ChildOf("other-harp"))
}
