package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// The report dedupe key was (harp, seq), but seq is a PER-RUN
// counter. Home.seq starts at 0 in each runner process, and a resume revokes
// the credential, severs the runner and spawns a fresh one — so run 2's first
// report arrives as seq 1, at or below run 1's watermark, and is silently
// discarded before it is ever journaled (recordSummary's pre-journal guard).
//
// Under one-shot driving, which mints a new run per TURN, that is essentially
// every report after turn 1: structured report-back goes silently lossy for
// exactly the delegation mode the project just shipped.
//
// itemsFold gets this right — its watermark is keyed by run_id (items.go).
// These tests hold reportsFold to the same contract.

// TestReportDedupe_SurvivesResume is the load-bearing assertion: two runs of
// the same harp each file a report at seq 1, and the SECOND must land.
func TestReportDedupe_SurvivesResume(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	if !assert.NoError(t, err) {
		return
	}
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	// Run 1 files its report at the fresh runner's seq 1.
	c.recordSummary(out.Harp, "run-1", 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "first run's finding",
	})
	if !assert.Contains(t, c.LatestReport(out.Harp), "first run's finding", "precondition: run 1's report must land") {
		return
	}

	// The resume: a NEW runner process, so its Home.seq restarts at 1.
	c.recordSummary(out.Harp, "run-2", 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "second run's finding",
	})

	assert.Contains(t, c.LatestReport(out.Harp), "second run's finding",
		"the resumed run's report was silently discarded: the dedupe watermark is keyed by harp, "+
			"but seq restarts at 1 in every new runner process")
}

// TestReportDedupe_StillDropsRedeliveryWithinARun keeps the property the
// watermark exists for: at-least-once redelivery on RECONNECT of the same run
// must still be deduped, not doubled.
func TestReportDedupe_StillDropsRedeliveryWithinARun(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	if !assert.NoError(t, err) {
		return
	}
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(out.Harp, "run-1", 2, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "the real report",
	})
	// The same run redelivers an EARLIER seq after a reconnect.
	c.recordSummary(out.Harp, "run-1", 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "a stale redelivery",
	})

	assert.Contains(t, c.LatestReport(out.Harp), "the real report",
		"an at-least-once redelivery below the same run's watermark overwrote the latest report")
}

// TestReportDedupe_IsPerHarp guards the other axis: keying on run_id alone
// must not let one harp's watermark suppress another's report.
func TestReportDedupe_IsPerHarp(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	a, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "a", "", "")
	if !assert.NoError(t, err) {
		return
	}
	b, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "b", "", "")
	if !assert.NoError(t, err) {
		return
	}
	require.Eventually(t, func() bool { return rosterState(c, b.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(a.Harp, "shared-run", 5, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "from a",
	})
	c.recordSummary(b.Harp, "shared-run", 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "from b",
	})

	assert.Contains(t, c.LatestReport(b.Harp), "from b",
		"one harp's report suppressed another's: the watermark must be keyed by (harp, run_id), not run_id alone")
}
