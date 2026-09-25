package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSetAgentRequest_CarriesEveryTypedFlag pins how each typed flag
// reaches the request: valid --surface pairs by their parsed kind, an
// unparseable set kept as written (so SetAgent's validation reports it
// rather than the flag vanishing), --root pairs trimmed, and the pointer
// flags as typed.
func TestBuildSetAgentRequest_CarriesEveryTypedFlag(t *testing.T) {
	parse := func(t *testing.T, args ...string) *cobra.Command {
		t.Helper()
		cmd := &cobra.Command{}
		registerAgentWriteFlags(cmd)
		require.NoError(t, cmd.Flags().Parse(args))
		return cmd
	}

	t.Run("valid surfaces", func(t *testing.T) {
		req := buildSetAgentRequest(parse(t, "--surface", "context=unsafe-file", "--surface", "skills=hook"), "dev")
		assert.Equal(t, map[string]string{"context": "unsafe-file", "skills": "hook"}, req.Surfaces)
	})
	t.Run("unparseable surfaces are kept as written", func(t *testing.T) {
		req := buildSetAgentRequest(parse(t, "--surface", "bogus", "--surface", " context = nope "), "dev")
		assert.Equal(t, map[string]string{"bogus": "", "context": "nope"}, req.Surfaces)
	})
	t.Run("roots are trimmed", func(t *testing.T) {
		req := buildSetAgentRequest(parse(t, "--root", " skills = engine-home ", "--root", "bare"), "dev")
		assert.Equal(t, map[string]string{"skills": "engine-home", "bare": ""}, req.Roots)
	})
	t.Run("pointer flags", func(t *testing.T) {
		req := buildSetAgentRequest(parse(t, "--profiles", "a,b", "--permissions", "plan", "--engine-home", "session"), "dev")
		require.NotNil(t, req.Profiles)
		assert.Equal(t, []string{"a", "b"}, *req.Profiles)
		require.NotNil(t, req.Permissions)
		assert.Equal(t, "plan", *req.Permissions)
		require.NotNil(t, req.HomeMode)
		assert.Equal(t, "session", *req.HomeMode)
		assert.Nil(t, req.Surfaces)
		assert.Nil(t, req.Roots)
	})
}
