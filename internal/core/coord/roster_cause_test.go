package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An ended child's roster entry says WHY it ended. Without the cause a child
// whose launch failed, or whose runner was lost, reads as phase "ended" and
// nothing else — indistinguishable from a clean finish to a coordinator that
// polls the roster instead of reading its mail.
func TestRoster_EndedChild_CarriesTerminalCauseAndDetail(t *testing.T) {
	for _, tc := range []struct {
		cause, detail string
	}{
		{CauseLaunchFailed, "engine binary not found"},
		{CauseRunnerLoss, "runner channel lost"},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			resetStrictness(t)
			sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
			c := newTestCoordinatorCap(t, sp, nil, 2)

			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
			require.NoError(t, err)
			require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)

			c.terminateRun(out.RunID, tc.cause, tc.detail)
			require.Eventually(t, func() bool { return c.runEnded(out.RunID) }, conformanceWait, 10*time.Millisecond)

			var held *RosterEntry
			for _, e := range c.Roster(ownerIdentity()) {
				if e.Harp == out.Harp {
					held = &e
				}
			}
			require.NotNil(t, held, "the ended child stays on the in-process roster")
			assert.Equal(t, StateEnded, held.State)
			assert.Equal(t, tc.cause, held.Cause)
			assert.Equal(t, tc.detail, held.Detail)

			snap := c.ListRuns(true, "")
			require.Len(t, snap.Runs, 1)
			assert.Equal(t, tc.cause, snap.Runs[0].Cause, "the wire roster projection carries the cause")
			assert.Equal(t, tc.detail, snap.Runs[0].Detail)
		})
	}
}

// The roster is the harp's LATEST run: a new run of an ended harp clears the
// previous run's cause, so a resumed child does not still read as failed.
func TestRosterFold_NewRunOfEndedHarp_ClearsTheCause(t *testing.T) {
	at := time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)
	f := newRosterFold()
	f.apply(factAt(factRunEnqueued, at, runEnqueued{RunID: "r1", Harp: "kid", Agent: "worker", ParentHarp: "owner"}))
	f.apply(factAt(factRunEnded, at, runEnded{RunID: "r1", Cause: CauseLaunchFailed, Detail: "boom"}))
	require.Equal(t, CauseLaunchFailed, f.snapshot()[0].Cause)

	f.apply(factAt(factRunEnqueued, at, runEnqueued{RunID: "r2", Harp: "kid", Agent: "worker", ParentHarp: "owner"}))
	got := f.snapshot()[0]
	assert.Equal(t, StateQueued, got.State)
	assert.Empty(t, got.Cause)
	assert.Empty(t, got.Detail)

	// A superseded attempt's late terminal does not stamp the live run.
	f.apply(factAt(factRunEnded, at, runEnded{RunID: "r1", Cause: CauseRunnerLoss}))
	assert.Empty(t, f.snapshot()[0].Cause)
}
