package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentInitiator is the fixture's coordinating agent: the owner, every
// child's parent.
func agentInitiator() ControlInitiator {
	return ControlInitiator{Kind: InitiatorAgent, Harp: ownerIdentity().Harp}
}

// TestPauseHold_OnlyTheHumanEndsTheHumansPause: a pause the human opened is
// the human's to end. An agent's resume is refused with a typed error saying
// so, and the run stays paused; the human's resume ends it.
func TestPauseHold_OnlyTheHumanEndsTheHumansPause(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)

	_, err = f.c.ControlResume(human(t), agentInitiator(), f.stranger)
	require.ErrorIs(t, err, ErrHumanPaused)
	assert.True(t, f.c.runPaused(f.runOf(t, f.stranger)), "the refused resume leaves the pause standing")

	newly, err := f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly)
}

// TestPauseHold_AnAgentsPauseEndsByThatAgentOrTheHuman: a pause an agent
// opened may be ended by that agent (or any initiator controlTarget admits
// over the child) and by the human.
func TestPauseHold_AnAgentsPauseEndsByThatAgentOrTheHuman(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), agentInitiator(), f.stranger, "waiting on a sibling")
	require.NoError(t, err)
	newly, err := f.c.ControlResume(human(t), agentInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly, "the agent ends its own pause")

	_, err = f.c.ControlPause(human(t), agentInitiator(), f.stranger, "waiting again")
	require.NoError(t, err)
	newly, err = f.c.ControlResume(human(t), humanInitiator(), f.stranger)
	require.NoError(t, err)
	assert.True(t, newly, "the human may end an agent's pause")
}

// TestHoldStop_AStoppedChildLeavesItsHold: agent_stop on a held child drops
// its harp from the hold (journaled), so a later send relaunches it normally
// — but its credential's hold is the credential's, not the child's: the run
// that comes up joins it, and resumes with the others when it releases.
func TestHoldStop_AStoppedChildLeavesItsHold(t *testing.T) {
	f, clk := newRateFixture(t)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)

	_, err := f.c.AgentStop(ownerIdentity(), f.sibling, "not needed", 0)
	require.NoError(t, err)
	f.awaitHold(t, f.worker)
	dropped := journaled[holdDropped](t, f.c, factHoldDropped)
	require.Len(t, dropped, 1)
	assert.Equal(t, f.sibling, dropped[0].Harp)

	_, disposition, err := f.c.peerSend(ownerIdentity(), f.sibling, KindMessage, "after the stop", nil, "")
	require.NoError(t, err)
	_, prose := deliveryDisposition(StateEnded)
	assert.Equal(t, prose, disposition, "a stopped child is relaunched by a send, held or not")
	f.awaitHold(t, f.worker, f.sibling)
	var launched []string
	for _, p := range journaled[holdParked](t, f.c, factHoldParked) {
		if p.Cause == "launch" {
			launched = append(launched, p.Harp)
		}
	}
	assert.Equal(t, []string{f.sibling}, launched, "the relaunched run joins its credential's hold as it comes up")

	clk.Advance(limitResets.Sub(clk.Now()))
	assert.Empty(t, f.c.CredentialHolds())
	awaitChatText(t, f.sp, 3, "after the stop")
}

// TestHoldStop_AStoppedChildLeavesItsPause: agent_stop on a paused child
// ends the pause with it (journaled), so a later send relaunches it.
func TestHoldStop_AStoppedChildLeavesItsPause(t *testing.T) {
	f, _ := newRateFixture(t)
	_, err := f.c.ControlPause(human(t), humanInitiator(), f.stranger, "reviewing")
	require.NoError(t, err)

	_, err = f.c.AgentStop(ownerIdentity(), f.stranger, "", 0)
	require.NoError(t, err)
	assert.False(t, f.c.harpHeld(f.stranger), "the stop takes the harp out of its pause")
	assertReleased(t, f.c, "empty")

	_, disposition, err := f.c.peerSend(ownerIdentity(), f.stranger, KindMessage, "after the stop", nil, "")
	require.NoError(t, err)
	_, prose := deliveryDisposition(StateEnded)
	assert.Equal(t, prose, disposition)
	awaitChatText(t, f.sp, 3, "after the stop")
}
