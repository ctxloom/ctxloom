package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestStartOwnedRun_ApprovalGuardRejectsInstantly_PinsDefect pins TODAY's
// actual behaviour of an owner-owned run's approval flow, ahead of the
// human-approval-surface change described in
// ~/.ctxloom/sessions/ugly-icy-squid/approval-surface.plan.md ("case (B): the
// owned run that is INSTANTLY REJECTED").
//
// THIS TEST DELIBERATELY PINS A DEFECT, not a feature. An owned run's
// stamped Depth equals the OWNER's own depth (0 — see StartOwnedRun's own
// doc comment on why), so Identity.IsChild() is FALSE for its own
// credential. serveApproval's very first statement (approval.go) refuses
// any !caller.IsChild() caller with codes.PermissionDenied — BEFORE it looks
// up the run record, BEFORE it walks the escalation ladder, and BEFORE its
// first c.audit call. EngineHost.resolveApproval (enginehost.go) then maps
// that non-OK status to decision == nil, and pickPermissionOption(options,
// false) silently picks a reject_once option — so the engine is told "the
// user refused", though no human, no ladder rung, and no audit entry were
// ever actually involved. The plan-preset ladder attached to this run would
// normally RELAY a COMMAND_EXECUTION request to the parent (proven by
// TestApproval_RelayRoundTrip for a delegated child) — it never gets the
// chance here.
//
// Assertions 2 and 3 below PASSING is the bug, captured: an approval that
// never touched the audit journal and never reached the parent's mailbox,
// despite silently determining the run's behaviour. A future fix is
// expected to flip at least one of them (most plausibly by making case (B)
// refuse loudly at startup instead of silently declining every approval —
// see the plan's "DECIDED 2026-08-11" section — or by giving the guard's
// affected path a real ladder walk). If a later change flips these
// assertions, that is progress, not a broken test — update/replace this
// test in the same change and explain why in the commit.
func TestStartOwnedRun_ApprovalGuardRejectsInstantly_PinsDefect(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Reuses ownerIdentity()'s own harp so assertNoMailKind (hardcoded to
	// ownerIdentity()) below checks the mailbox this test's owner actually
	// owns.
	ownerHarp := ownerIdentity().Harp
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	require.Equal(t, 0, owner.Depth, "the session owner is depth 0")

	permReq := commandExecRequest("perm-1")
	sc := &scriptedChat{permission: permReq}
	starter, started := ownerRunStarter(ctx, sc, "claude-code")

	// Permission "plan" so the run's Ladder is presetLadder(plan) — the same
	// preset TestApproval_RelayRoundTrip proves relays COMMAND_EXECUTION to
	// the parent for a delegated CHILD. Here the guard fires before that
	// ladder is ever consulted.
	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionPlan,
	}, starter, "hello owner run")
	require.NoError(t, err)
	require.True(t, *started, "StartOwnedRun must launch the runner via the starter")
	require.NotEmpty(t, outcome.RunID)

	// 1. The engine receives the REJECT option — read straight off the
	// scripted chat's recorded answers — though no human and no ladder rung
	// ever decided anything.
	require.Eventually(t, func() bool {
		return len(sc.recordedAnswers()) == 1
	}, 10*time.Second, 20*time.Millisecond, "the guard's synthetic denial must still answer the engine's permission request")
	ans := sc.recordedAnswers()[0]
	assert.Equal(t, "perm-1", ans.ID)
	assert.Equal(t, "reject-1", ans.OptionID,
		"PINNED DEFECT: the engine is told the user refused, though the !caller.IsChild() guard fired before any ladder rung ran")

	// 2. PINNED DEFECT: the approval audit journal is EMPTY for this
	// attempt. serveApproval returns above the ladder's `for i, rung :=
	// range rungs` loop, so its first c.audit call (auto_accept/
	// auto_decline/relay/bottom) never executes.
	entries := readApprovalAudit(t, c)
	assert.Empty(t, entries,
		"PINNED DEFECT: the guard leaves ZERO audit trail of a decision that determined the run's behaviour — a security-relevant silent gap")

	// 3. PINNED DEFECT: no approval_request mail was ever queued to the
	// parent. The ladder never ran, so relayApproval (the only place that
	// calls queueMailPayloadID for an approval) was never reached.
	assertNoMailKind(t, c, "approval_request", 300*time.Millisecond)
}
