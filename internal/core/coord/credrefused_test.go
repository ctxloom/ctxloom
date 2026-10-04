package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// refusedFinding is what leads every finding and notice for a refused
// credential.
const refusedFinding = "CREDENTIAL REFUSED"

// TestCredentialHold_ARefusalParksTheCredentialsRunsUntilTheHumanReleases is
// the row's SETTLES: one child's turn is refused its credential, so every run
// sharing it is parked with NO deadline — no timer, still in force past every
// backoff cap — with ONE finding naming the remedy for where the credential
// comes from; the roster shows the hold's kind; an agent may not release it;
// the human's resume does, and the mail held meanwhile runs.
func TestCredentialHold_ARefusalParksTheCredentialsRunsUntilTheHumanReleases(t *testing.T) {
	f, clk := newRateFixture(t)

	f.send(t, f.worker, credRefused+" do the work")
	hold := f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	assert.Equal(t, agent.FailureCredentialRejected, hold.Kind)
	assert.True(t, hold.Until.IsZero(), "a refused credential has no deadline: %v", hold.Until)
	assert.Zero(t, clk.Pending(), "no timer: nothing but a human (or a re-authenticated restart) releases it")

	// The hold and its parks are journaled before the finding is raised;
	// the fold's completion is what orders the finding before this read.
	f.awaitFolds(t, 1)
	require.Equal(t, 1, f.findingsWith(refusedFinding), "ONE finding for the hold: %v", f.findings.All())
	assert.Equal(t, 1, f.findingsWith("export a fresh CLAUDE_CODE_OAUTH_TOKEN"), "the remedy names the variable to refresh: %v", f.findings.All())
	assert.Equal(t, 1, f.findingsWith("ctxloom run --session "+ownerIdentity().Harp), "and the restart that picks it up: %v", f.findings.All())
	assert.Zero(t, f.findingsWith(refusedToken), "never the value")

	opened := assertOpened(t, f.c, agent.FailureCredentialRejected, holdScopeCredential)
	require.Len(t, opened, 1)
	assert.True(t, opened[0].Until.IsZero())
	assert.Equal(t, f.sp.credentialOf("worker").Fingerprint(), opened[0].Fingerprint,
		"the hold keeps the refused credential's fingerprint, for a restart to compare")

	for _, harp := range []string{f.worker, f.sibling} {
		got := f.holdOf(t, harp)
		require.NotNil(t, got, "%s is held", harp)
		assert.Equal(t, "credential_rejected", got.Kind)
		assert.True(t, got.Until.IsZero())
	}
	assert.Nil(t, f.holdOf(t, f.stranger), "a run on another credential is not held")

	f.send(t, f.sibling, "held work")
	_, err := f.c.ControlResume(human(t), ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}, f.sibling)
	require.ErrorIs(t, err, ErrCredentialHeld, "an agent may not release a refused credential's hold")
	clk.Advance(rateLimitCap + time.Hour)
	require.Len(t, f.c.CredentialHolds(), 1, "no backoff ever releases it")

	_, err = f.c.ControlResume(human(t), humanInitiator(), f.sibling)
	require.NoError(t, err)
	assert.Empty(t, f.c.CredentialHolds())
	assertReleased(t, f.c, HoldKindHuman)
	awaitChatText(t, f.sp, 1, "held work")
}

// TestCredentialHold_ARefusalJoiningARateLimitUpgradesTheHold forces the
// order: a rate limit opens the credential's timed hold, then a sibling's
// turn that was already inside its engine is refused. The hold UPGRADES — its
// kind becomes credential_rejected, its timer is disarmed, and the refusal's
// finding is raised — so the limit's reset no longer releases it.
func TestCredentialHold_ARefusalJoiningARateLimitUpgradesTheHold(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(1)
	f.send(t, f.sibling, credRefused+" slow work")
	awaitChatText(t, f.sp, 1, "slow work")
	f.send(t, f.worker, limitHit+" do the work")
	timed := f.awaitHold(t, f.worker, f.sibling)
	require.Equal(t, agent.FailureRateLimited, timed.Kind)
	require.Equal(t, 1, clk.Pending())

	release()
	f.awaitFolds(t, 2)
	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.Equal(t, agent.FailureCredentialRejected, hold.Kind, "a refusal outranks a rate limit")
	assert.True(t, hold.Until.IsZero(), "the upgraded hold has no deadline: %v", hold.Until)
	assert.Zero(t, clk.Pending(), "its timer is disarmed")
	assert.Equal(t, 1, f.findingsWith(refusedFinding), "the upgrade tells the human, once: %v", f.findings.All())
	ext := journaled[holdExtended](t, f.c, factHoldExtended)
	require.NotEmpty(t, ext)
	assert.Equal(t, string(agent.FailureCredentialRejected), ext[len(ext)-1].Kind)
	assert.True(t, ext[len(ext)-1].Until.IsZero())

	clk.Advance(limitResets.Sub(clk.Now()) + time.Minute)
	assert.Len(t, f.c.CredentialHolds(), 1, "the limit's reset no longer releases it")
	_, err := f.c.ControlResume(human(t), ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}, f.worker)
	require.ErrorIs(t, err, ErrCredentialHeld)
}

// TestCredentialHold_ARateLimitJoiningARefusalLeavesItHumanOnly is the
// converse order: a rate limit on a credential already refused neither gives
// the hold a deadline nor relabels it.
func TestCredentialHold_ARateLimitJoiningARefusalLeavesItHumanOnly(t *testing.T) {
	f, clk := newRateFixture(t)
	release := f.gate(1)
	f.send(t, f.sibling, limitHit+" slow work")
	awaitChatText(t, f.sp, 1, "slow work")
	f.send(t, f.worker, credRefused+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	release()
	f.awaitFolds(t, 2)

	hold := f.awaitHold(t, f.worker, f.sibling)
	assert.Equal(t, agent.FailureCredentialRejected, hold.Kind)
	assert.True(t, hold.Until.IsZero())
	assert.Zero(t, clk.Pending())
	assert.Empty(t, journaled[holdExtended](t, f.c, factHoldExtended), "nothing about the hold changed")
	assert.Equal(t, 1, f.findingsWith(refusedFinding))
}

// The turn-idle value of a refused turn is a held failure: the runner parks
// itself on it, and the coordinator folds it into the credential's hold.
func TestTurnFailureOf_ARefusedCredentialIsHeld(t *testing.T) {
	assert.True(t, HoldsFailure(agent.FailureCredentialRejected))
	assert.Equal(t, &agent.TurnFailure{Kind: agent.FailureCredentialRejected},
		turnFailureOf(map[string]any{"stop_reason": "credential_rejected"}))
	assert.True(t, holdDeadline(time.Now(), agent.TurnFailure{Kind: agent.FailureCredentialRejected}).IsZero(),
		"a refused credential's hold has no deadline")
}

// The remedy follows where the credential comes from: a captured variable
// needs a fresh export and a restart (a running process cannot see it); a
// store read in place needs a sign-in, then the human's resume.
func TestRefusedCredentialRemedy(t *testing.T) {
	env := engine.Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "x"}}.Source("claude")
	got := RefusedCredentialRemedy(env, "brave-harp")
	assert.Contains(t, got, "export a fresh CLAUDE_CODE_OAUTH_TOKEN")
	assert.Contains(t, got, "ctxloom run --session brave-harp")

	store := engine.Credentials{Stores: []engine.SharedStore{{HomeRel: ".claude"}}}.Source("claude")
	got = RefusedCredentialRemedy(store, "brave-harp")
	assert.Contains(t, got, "sign in again (~/.claude)")
	assert.Contains(t, got, "resume")
	assert.NotContains(t, got, "--session", "a store refresh reaches a running session")
}
