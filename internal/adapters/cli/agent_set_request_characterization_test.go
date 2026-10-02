package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSetAgentRequest_CarriesEveryTypedFlag pins how each typed flag
// reaches the request: --surface pairs as typed, trimmed (SetAgent judges
// them against the engine, so an unknown or retired name is reported there
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
		req, err := buildSetAgentRequest(parse(t, "--surface", "context=unsafe-file", "--surface", "skills=hook"), "dev")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"context": "unsafe-file", "skills": "hook"}, req.Surfaces)
	})
	t.Run("a retired approach is carried to SetAgent, which refuses it typed", func(t *testing.T) {
		req, err := buildSetAgentRequest(parse(t, "--surface", "settings=hew-record"), "dev")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"settings": "hew-record"}, req.Surfaces)
	})
	t.Run("a kind named two different ways is refused", func(t *testing.T) {
		// A map keeps the last pair, so passing this through would make
		// `--surface context=hook --surface context=unsafe-file` do something
		// the command line does not say.
		_, err := buildSetAgentRequest(parse(t, "--surface", "context=hook", "--surface", "context=unsafe-file"), "dev")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context twice")
	})
	t.Run("the same pair repeated is one preference", func(t *testing.T) {
		req, err := buildSetAgentRequest(parse(t, "--surface", "context=hook", "--surface", " context = hook "), "dev")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"context": "hook"}, req.Surfaces)
	})
	t.Run("unparseable surfaces are kept as written", func(t *testing.T) {
		req, err := buildSetAgentRequest(parse(t, "--surface", "bogus", "--surface", " context = nope "), "dev")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"bogus": "", "context": "nope"}, req.Surfaces)
	})
	t.Run("roots are trimmed", func(t *testing.T) {
		req, err := buildSetAgentRequest(parse(t, "--root", " skills = engine-home ", "--root", "bare"), "dev")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"skills": "engine-home", "bare": ""}, req.Roots)
	})
	t.Run("pointer flags", func(t *testing.T) {
		req, err := buildSetAgentRequest(parse(t, "--profiles", "a,b", "--permissions", "plan", "--engine-home", "session"), "dev")
		require.NoError(t, err)
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
