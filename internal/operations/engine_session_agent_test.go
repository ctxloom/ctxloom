package operations

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
)

// `ctxloom acp --agent <unknown>` silently degraded to a generic
// session. An agent binding carries engine, profiles, RUNTIME (host vs
// container) and PERMISSIONS, so substituting a generic session drops all of
// it — a typo'd `--agent codr` for an agent declared `runtime: container`
// would run on the HOST while the user believed they were isolated. The only
// signal was one stderr line most editors bury in a log; it already cost a
// 40-minute misdiagnosis.
//
// An EXPLICIT request that cannot be honored must fail. The implicit
// cfg.DefaultAgent binding keeps degrading — a project that merely SET
// default_agent never asked for this session, so hard-breaking it would punish
// a config the editor did not choose.
func TestExplicitAgentResolutionFailureIsFatal(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{Agents: map[string]agents.Agent{
		"coder":       {Profiles: []string{"p"}},
		"coordinator": {Profiles: []string{"p"}},
	}})

	t.Run("explicit --agent that does not resolve is a fatal, actionable error", func(t *testing.T) {
		err := agentBindingError(cfg, "codr", errors.New(`agent "codr" not found`))
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "codr", "name the agent that was asked for")
		assert.Contains(t, msg, "coder", "list what IS available so the typo is obvious")
		assert.Contains(t, msg, "coordinator")
		assert.Contains(t, msg, "--agent", "name the fix")
	})

	t.Run("the error explains what silently degrading would have cost", func(t *testing.T) {
		err := agentBindingError(cfg, "codr", errors.New("nope"))
		msg := err.Error()
		assert.Contains(t, msg, "runtime")
		assert.Contains(t, msg, "permissions")
	})

	t.Run("a project with no agents at all still produces a usable error", func(t *testing.T) {
		err := agentBindingError(&config.Config{}, "codr", errors.New("nope"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agents are defined")
	})
}
