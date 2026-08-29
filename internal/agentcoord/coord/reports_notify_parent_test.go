package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// A REPORT AND A MESSAGE LIVE IN DIFFERENT STORES, and that divergence read as
// lost delivery. recordSummary journals a factSummary into the REPORTS fold —
// what roster reads — and queued nothing. So a parent could see "FINAL: ..." in
// roster for a report agent_recv had nothing to return, and a child filing the
// report its own instructions call "the deliverable" was, from a waiting
// parent's view, completely silent.
//
// Asserts the EFFECT — mail actually pending for the parent — rather than that
// a notification was emitted.
func TestFinalReport_IsQueuedToTheParent(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)
	owner := ownerIdentity()

	out, err := c.AgentRun(context.Background(), owner, "worker", "do the thing", "", "")
	if !assert.NoError(t, err) {
		return
	}
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	before := c.pendingCount(owner.Harp)
	c.recordSummary(out.Harp, out.RunID, 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_FINAL,
		Text:  "FINAL: the finding the parent is waiting for",
	})

	assert.Greater(t, c.pendingCount(owner.Harp), before,
		"a child's FINAL report must reach its parent's MAILBOX, not only the reports fold")
}

// The control that stops the above from being satisfiable by queueing on every
// report. Waking a parent on each PROGRESS or STEP is a flood, not a doorbell —
// under one-shot driving those arrive constantly — so only FINAL may queue.
func TestProgressReport_IsNotQueuedToTheParent(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)
	owner := ownerIdentity()

	out, err := c.AgentRun(context.Background(), owner, "worker", "do the thing", "", "")
	if !assert.NoError(t, err) {
		return
	}
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	before := c.pendingCount(owner.Harp)
	c.recordSummary(out.Harp, out.RunID, 1, &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "still working",
	})

	assert.Equal(t, before, c.pendingCount(owner.Harp),
		"a PROGRESS report must not wake the parent; only FINAL is the completion contract")

	// And the report itself still lands in the fold — the queueing is additive,
	// not a replacement for journaling.
	assert.Contains(t, c.LatestReport(out.Harp), "still working")
}
