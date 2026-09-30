package coord

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// A run's credential is checked when a call starts, and a call can outlive
// its run: an agent_report or agent_recv that is RECORDED after the run ended
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

func TestAgentRecv_FromAnEndedRunIsRefused(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	ownerHarp := ownerIdentity().Harp
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	out, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass"), false), starter, "do the thing")
	require.NoError(t, err)
	require.True(t, *started)
	run := Identity{Harp: ownerHarp, RunID: out.RunID}

	_, err = c.AgentRecv(ctx, run, 0)
	require.ErrorIs(t, err, ErrRecvTimeout, "a live run's receive is served")

	c.terminateRun(out.RunID, CauseStopped, "test terminal")
	_, err = c.AgentRecv(ctx, run, 0)
	require.ErrorIs(t, err, ErrRevoked, "a receive recorded after its run ended is refused")

	_, err = c.AgentRecv(ctx, owner, 0)
	require.ErrorIs(t, err, ErrRecvTimeout, "the session-owner credential serves the harp, not the run, and stays valid")
}

// TestSpoolInboxRecv_RunEndingBeforeItParksIsRefused forces the park window:
// the run is live when the receive starts and has ended by the time it would
// park. The park is where the receive is recorded, so it is refused there and
// leaves no poll behind for a sever that has already run.
func TestSpoolInboxRecv_RunEndingBeforeItParksIsRefused(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-ends-parking"
	var checks atomic.Int32
	live := func(string, string) bool { return checks.Add(1) == 1 } // live only at call start
	in := newSpoolInbox(report.To(termSink()), spool.NewHomeMapper(), &SpoolDeliveryCounters{},
		func(string, string) {}, func(string, string) {}, live)

	_, err := in.recv(context.Background(), harp, "run-ending", 5*time.Second)
	require.ErrorIs(t, err, ErrRevoked)
	assert.False(t, in.parked(harp), "a refused receive parks nothing")
}
