package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// StartOwnedRun validated Harp, Backend and start — but not the
// prompt. An empty prompt reached issueStartRun, which builds `input` only
// `if first != ""`, so StartRun was issued with a NIL Input, round-tripped
// fine, and StartOwnedRun returned a populated RunOutcome and nil. A
// top-level container run, zero payload delivered, every signal green — the
// same shape as the known `runtime:container` prompt-delivery defect, and
// ctxloom's characteristic silent no-op.
//
// The discriminator is Oneshot. A --one-shot single-turn run gets exactly ONE
// turn and this prompt is it, so an empty one can only ever be a delivery
// failure. A STRUCTURED run legitimately opens with no lead and takes its
// turns via SendOwnedRunTurn, so an empty prompt there is "nothing to do,
// legitimately" and must still succeed.

// TestStartOwnedRun_RejectsEmptyOneshotPrompt is the load-bearing assertion.
func TestStartOwnedRun_RejectsEmptyOneshotPrompt(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-empty-prompt"
	token, err := c.RegisterSessionOwner(ownerHarp)
	if !assert.NoError(t, err) {
		return
	}
	owner, ok := c.Identify(token)
	if !assert.True(t, ok) {
		return
	}

	sc := &scriptedChat{}
	starter, started := ownerRunStarter(ctx, sc, "claude-code")

	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
		Oneshot:    true,
	}, starter, "")

	assert.Error(t, err,
		"a one-shot owner run with an empty prompt reported success while delivering no payload at all")
	assert.Nil(t, outcome, "a refused run must not hand back a populated RunOutcome")
	assert.False(t, *started,
		"the refusal must land BEFORE a container runner is launched — a zero-payload run should cost nothing")
}

// TestStartOwnedRun_AllowsEmptyStructuredPrompt is the discriminator's other
// half: a structured session that opens with no lead and drives itself via
// SendOwnedRunTurn is legitimately empty and must not be converted to a
// failure.
func TestStartOwnedRun_AllowsEmptyStructuredPrompt(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-structured-empty"
	token, err := c.RegisterSessionOwner(ownerHarp)
	if !assert.NoError(t, err) {
		return
	}
	owner, ok := c.Identify(token)
	if !assert.True(t, ok) {
		return
	}

	sc := &scriptedChat{}
	starter, _ := ownerRunStarter(ctx, sc, "claude-code")

	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "")
	require.NoError(t, err, "a structured owner run takes its turns via SendOwnedRunTurn: an empty lead is legitimate")
	assert.NotNil(t, outcome)
}

// TestStartRunPayloadErr_Discriminates pins the second half of the finding —
// issueStartRun's own guard, which is what actually stops a StartRun with a
// nil Input going out on the wire. It must distinguish a run that has some
// other source of work (a resume key, or mail waiting to be drained as its
// first turn) from one that has literally nothing.
func TestStartRunPayloadErr_Discriminates(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	child := &childRt{harp: "payload-child", agentName: "worker"}

	assert.NoError(t, c.startRunPayloadErr(child, "do the thing", ""),
		"a composed first turn is payload")
	assert.NoError(t, c.startRunPayloadErr(child, "", "harness-session-123"),
		"a resume continues the engine's own recorded session: no lead needed")

	owner := &childRt{harp: "payload-owner", agentName: "owner", ownerRun: true}
	assert.NoError(t, c.startRunPayloadErr(owner, "", ""),
		"an owner run's emptiness is adjudicated by StartOwnedRun, not here")

	assert.Error(t, c.startRunPayloadErr(child, "", ""),
		"a delegated child with no lead, no resume key and no queued mail would be started "+
			"with a nil Input — zero payload, every signal green")

	// ...unless mail is waiting: the standup drain delivers it as the first turn.
	_, _, err := c.queueMail("someone", child.harp, "message", "your first turn")
	if !assert.NoError(t, err) {
		return
	}
	assert.NoError(t, c.startRunPayloadErr(child, "", ""),
		"queued mail IS the first turn (issueStartRun's standup drain): that run has work to do")
}
