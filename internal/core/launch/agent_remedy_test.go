package launch

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// remedyOf pulls the one-line fix a refusal carries, the way the CLI's error
// tail does, so each case asserts the command the user is told to run.
func remedyOf(t *testing.T, err error) string {
	t.Helper()
	var r report.Remediable
	require.True(t, errors.As(err, &r), "a refused agent lookup names its fix: %v", err)
	return r.Remedy()
}

// TestBindingSelection_MissingAgent_NamesOneRunnableFix: a launch whose agent
// does not resolve is refused with ErrNoAgent and ONE next command that
// exists in the CLI. The three situations need different commands: outside
// any project there is nothing to bind until `init` scaffolds one; a project
// with agents but no default needs one chosen; anything else needs one made.
func TestBindingSelection_MissingAgent_NamesOneRunnableFix(t *testing.T) {
	someAgent := map[string]agents.Agent{"dev": {Profiles: []string{"default"}}}
	cases := []struct {
		name string
		cfg  config.Fixture
		ask  string
		fix  string
	}{
		{"bare launch outside any project", config.Fixture{Source: config.SourceHome}, "", noProjectAgentFix},
		{"bare launch, agents exist but none is default", config.Fixture{Agents: someAgent}, "", chooseDefaultAgentFix},
		{"bare launch, no agents at all", config.Fixture{}, "", createAgentFix},
		{"named agent that does not exist", config.Fixture{Agents: someAgent}, "nosuch", createAgentFix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := bindingSelection(config.NewFixture(tc.cfg), tc.ask, false)
			require.ErrorIs(t, err, ErrNoAgent)
			assert.Equal(t, tc.fix, remedyOf(t, err))
		})
	}
}

// TestBindingSelection_MissingAgent_NeverQuotesAnEmptyName: a bare launch has
// no name to quote, and `"" ` in an error reads as a bug in ctxloom.
func TestBindingSelection_MissingAgent_NeverQuotesAnEmptyName(t *testing.T) {
	_, err := bindingSelection(config.NewFixture(config.Fixture{}), "", false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), `""`)
}
