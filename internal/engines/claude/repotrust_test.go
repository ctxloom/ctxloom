package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// An untrusted repository's turn reads only the user source — ctxloom's
// session home, where its own hooks live — and only the MCP servers
// ctxloom hands it on --mcp-config: the repository's settings, hooks and
// .mcp.json do not load.
func TestTurnArgv_UntrustedRepoLoadsOnlyUserSources(t *testing.T) {
	args, err := headlessTurnArgv(t, modePolicy(modeDefault), engine.Turn{Posture: engine.TurnPosture{Trust: engine.TrustUntrusted}})
	require.NoError(t, err)
	assert.True(t, argPair(args, flagSettingSources, "user"), "untrusted: %v", args)
	assert.Contains(t, args, flagStrictMCPConfig)
}

// The zero posture is untrusted: a turn nobody gave a verdict fails closed.
func TestTurnArgv_NoVerdictIsUntrusted(t *testing.T) {
	args, err := headlessTurnArgv(t, modePolicy(modeBypass), engine.Turn{})
	require.NoError(t, err)
	assert.True(t, argPair(args, flagSettingSources, "user"), "no verdict: %v", args)
	assert.Contains(t, args, flagStrictMCPConfig)
}

// A verdict the engine does not know is not trust.
func TestTurnArgv_UnknownVerdictIsUntrusted(t *testing.T) {
	args, err := headlessTurnArgv(t, modePolicy(modeDefault), engine.Turn{Posture: engine.TurnPosture{Trust: engine.TrustTrusted + 1}})
	require.NoError(t, err)
	assert.True(t, argPair(args, flagSettingSources, "user"), "unknown verdict: %v", args)
	assert.Contains(t, args, flagStrictMCPConfig)
}

// A trusted repository's turn keeps claude's default sources: its own
// settings, hooks and MCP servers load.
func TestTurnArgv_TrustedRepoKeepsDefaultSources(t *testing.T) {
	args, err := headlessTurnArgv(t, modePolicy(modeDefault), engine.Turn{Posture: engine.TurnPosture{Trust: engine.TrustTrusted}})
	require.NoError(t, err)
	assert.NotContains(t, args, flagSettingSources)
	assert.NotContains(t, args, flagStrictMCPConfig)
}

// A --settings flag is a source --setting-sources does not filter, so a
// presentation naming a settings file would load the repository's own
// .claude/settings.json into a turn whatever its verdict. The turn's
// posture is its one --settings; a presentation naming another is refused
// even when the posture has nothing to say (bypass), which is the case the
// second-settings check alone let through.
func TestTurnArgv_RefusesAPresentedSettingsFile(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	for _, trust := range []engine.WorkspaceTrust{engine.TrustUntrusted, engine.TrustTrusted} {
		_, err := headlessTurnArgv(t, modePolicy(modeBypass), engine.Turn{Posture: engine.TurnPosture{Trust: trust}}, file)
		assert.ErrorIs(t, err, errTurnSettingsPresented, "trust=%d", trust)
	}
}
