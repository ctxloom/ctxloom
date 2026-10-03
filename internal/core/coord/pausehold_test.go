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
