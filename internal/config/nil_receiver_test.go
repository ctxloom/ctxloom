package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U048-F10: five methods on *Config explicitly guard `c == nil` while the rest
// panic, so a caller cannot tell which contract applies to the method in front
// of it.
//
// The observation is exact — measured: 93 methods on *Config, 73 exported,
// five nil-tolerant, and the census-era five are still exactly today's five.
// The remedy is NOT to unify. Making the other 88 nil-tolerant converts a
// caller bug ("config was never loaded") into a silently empty answer, which is
// this codebase's characteristic failure mode; removing the five would break
// callers that legitimately hold nil. So the contract is stated on the type and
// closed HERE, which is what actually answers "callers cannot know".
//
// Worth recording for whoever decides this properly: the four isolation
// accessors' guards are currently redundant. Their only composite caller,
// operations.IsolationImageConfig, already returns early on a nil cfg — and it
// has to, because it also calls GetAppRoot, GetIsolationDevcontainerService and
// GetIsolationEngines, none of which are nil-safe. So the divergence is
// defensible but, on the measured paths, unexercised.
func TestConfig_NilReceiverContract(t *testing.T) {
	var c *Config

	t.Run("the_five_documented_methods_tolerate_nil", func(t *testing.T) {
		require.NotPanics(t, func() {
			doc, err := c.MarshalYAML()
			assert.NoError(t, err)
			assert.Nil(t, doc)
			assert.Empty(t, c.IsolationImageFor("claude-code"))
			assert.Empty(t, c.IsolationBaseContainerfilePath())
			assert.True(t, c.IsolationDevcontainerBaseEnabled(),
				"the devcontainer base defaults ON, and a nil config must report the default rather than the zero value")
			assert.Nil(t, c.DefaultAgentProfiles())
		})
	})

	// The negative half is what makes the set CLOSED rather than merely
	// documented. If a future edit adds a nil guard to an ordinary accessor,
	// this fails and the author has to come and widen the doc deliberately.
	t.Run("an_ordinary_accessor_does_not_tolerate_nil", func(t *testing.T) {
		assert.Panics(t, func() { _ = c.GetAppRoot() },
			"a nil *Config means config was never loaded — that is a caller bug, and ordinary accessors must keep saying so loudly")
		assert.Panics(t, func() { _ = c.GetDefaultLLM() })
	})
}
