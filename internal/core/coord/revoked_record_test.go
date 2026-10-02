package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run's credential is checked when a call starts, and a call can outlive
// its run: an agent_report that is RECORDED after the run ended
// speaks for nothing and must be refused, however valid the credential was on
// arrival. These tests present the ended run's identity — what Identify
// returned for its credential while it was live.

func TestReport_FromAnEndedRunIsRefused(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, startRunSpawner(nil), nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}

	require.NoError(t, c.Report(context.Background(), child, ReportRequest{Scope: "progress", Body: "while live"}),
		"a live run's report is recorded")
	assert.Contains(t, c.LatestReport(out.Harp), "while live")

	c.terminateRun(out.RunID, CauseStopped, "test terminal")
	err = c.Report(context.Background(), child, ReportRequest{Scope: "progress", Body: "after it ended"})
	require.ErrorIs(t, err, ErrRevoked, "a report recorded after its run ended is refused")
	assert.NotContains(t, c.LatestReport(out.Harp), "after it ended", "and nothing of it is journaled")
}
