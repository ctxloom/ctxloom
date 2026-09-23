package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
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
			sp := newFakeSpawner(nil, nil)
			c := newTestCoordinator(t, sp, nil)
			ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
			defer cancel()

			const ownerHarp = "owner-harp"
			token, err := c.RegisterSessionOwner(ownerHarp)
			require.NoError(t, err)
			owner, ok := c.Identify(token)
			require.True(t, ok)

			starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
			out, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", agent.PermissionBypass), false), starter, "do the thing")
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
