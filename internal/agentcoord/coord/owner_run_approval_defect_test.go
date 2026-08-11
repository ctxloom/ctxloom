package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestStartOwnedRun_ApprovalGuardCancels covers an owner-owned run's approval
// flow at the !caller.IsChild() guard.
//
// An owned run's stamped Depth equals the OWNER's own depth (0 — see
// StartOwnedRun's own doc comment on why), so Identity.IsChild() is FALSE for
// its own credential. serveApproval's very first statement (approval.go)
// refuses any !caller.IsChild() caller with codes.PermissionDenied — BEFORE
// it looks up the run record, BEFORE it walks the escalation ladder, and
// BEFORE its first c.audit call. EngineHost.resolveApproval (enginehost.go)
// maps that non-OK status to decision == nil: no human, no ladder rung and no
// audit entry were ever involved, so NOBODY DECIDED.
//
// Assertion 1 is wiry-judge's fix, and this test was written to pin the
// defect it replaces: resolveApproval used to run pickPermissionOption(
// options, false) on this path and answer "reject-1", which claude-code-acp
// renders to the model — and to the durable transcript — as
// {behavior:"deny", message:"User refused permission to run tool"}. ctxloom's
// own refusal, falsely attributed to the operator. The engine is now told the
// request was CANCELLED (empty OptionID -> ACP {outcome:"cancelled"}), which
// is the honest report of "nobody answered".
//
// Assertions 2 and 3 still pin a REAL, UNFIXED GAP, deliberately: this
// approval never touched the audit journal and never reached the parent's
// mailbox, though it determined the run's behaviour — a security-relevant
// silent gap. wiry-judge only made the ANSWER honest; giving the guard's
// affected path a real ladder walk (or refusing loudly at startup) is still
// open. If a later change flips assertion 2 or 3, that is progress, not a
// broken test — update this test in the same change and explain why in the
// commit.
func TestStartOwnedRun_ApprovalGuardCancels(t *testing.T) {
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

	// 1. The engine receives a CANCEL — no option at all — read straight off
	// the scripted chat's recorded answers: no human and no ladder rung ever
	// decided anything, so nothing may be reported as a decision.
	require.Eventually(t, func() bool {
		return len(sc.recordedAnswers()) == 1
	}, 10*time.Second, 20*time.Millisecond, "the guard's refusal must still answer the engine's permission request — never a hang")
	ans := sc.recordedAnswers()[0]
	assert.Equal(t, "perm-1", ans.ID)
	assert.Empty(t, ans.OptionID,
		"the !caller.IsChild() guard fired before any ladder rung ran, so the engine is told CANCELLED — never that the user refused")

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
