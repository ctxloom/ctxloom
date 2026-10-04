package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A few methods on *Config explicitly guard `c == nil` while the rest panic,
// so a caller cannot tell which contract applies to the method in front of it.
// The remedy is NOT to unify: making every accessor nil-tolerant converts a
// caller bug ("config was never loaded") into a silently empty answer, which
// is this codebase's characteristic failure mode. So the contract is stated on
// the Config type and closed HERE, which is what actually answers "callers
// cannot know".
func TestConfig_NilReceiverContract(t *testing.T) {
	var c *Config

	t.Run("the_documented_methods_tolerate_nil", func(t *testing.T) {
		require.NotPanics(t, func() {
			doc, err := c.MarshalYAML()
			assert.NoError(t, err)
			assert.Nil(t, doc)
			assert.Empty(t, c.IsolationImageFor("claude-code"))
			assert.Empty(t, c.IsolationBase(), "a nil config reports isolation_base unset")
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
