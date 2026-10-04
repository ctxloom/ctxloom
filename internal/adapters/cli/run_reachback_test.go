package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestStartOwnedRun_NilCoordinatorRefusesTheLaunch pins the owner run's
// refusal to start without a hosted coordinator.
//
// The coordinator IS the run's transport: the runner receives its Launch from
// it and the session is driven over its event stream, so a run launched
// without one has no transport at all and could only produce a runner that
// starts, answers nobody, and reports success. This must stay a hard refusal
// even in --degraded mode, which downgrades the coordinator standup failure
// elsewhere -- degrading THIS is not "fewer features", it is a run that
// cannot work.
func TestStartOwnedRun_NilCoordinatorRefusesTheLaunch(t *testing.T) {
	started := false
	sess, err := startOwnedRun(t.Context(), nil, ownedRunLaunch{
		Launch: launch.Launch{Identity: sessions.Identity{Harp: "swift-amber-falcon"}, Engine: "claude-code"},
	}, func(context.Context, map[string]string) (coord.OwnedRunner, error) {
		started = true
		return coord.OwnedRunner{}, nil
	})

	require.Error(t, err, "no coordinator means no transport — the launch must refuse, not proceed")
	assert.Nil(t, sess)
	assert.False(t, started, "nothing may be started before the refusal")
	assert.Contains(t, err.Error(), "coordinator",
		"the refusal must name what is missing, so the operator can act on it")
}

// TestRunState_MayDelegateIsTheBoundBindings: the root hands coord the
// may_delegate of the binding it launched under — --agent's, or the default
// agent's for a bare launch — and none for an assembly that bound no agent.
func TestRunState_MayDelegateIsTheBoundBindings(t *testing.T) {
	saved := []string{runAgent, runProfile}
	t.Cleanup(func() { runAgent, runProfile = saved[0], saved[1] })
	st := &runState{cfg: config.NewFixture(config.Fixture{
		DefaultAgent: "dev",
		Agents: map[string]agents.Agent{
			"dev":  {MayDelegate: []string{"finder"}},
			"lead": {MayDelegate: []string{"coder", "finder"}},
		},
	})}

	runAgent, runProfile = "lead", ""
	assert.Equal(t, []string{"coder", "finder"}, st.mayDelegate(), "--agent's binding")
	runAgent = ""
	assert.Equal(t, []string{"finder"}, st.mayDelegate(), "a bare launch binds the default agent")
	runProfile = "some-profile"
	assert.Empty(t, st.mayDelegate(), "an explicit assembly bound no agent")
}
