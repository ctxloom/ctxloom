package claudeengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

func TestHosting_Validates(t *testing.T) {
	require.NoError(t, Hosting().Validate())
}

func TestHosting_NameIsTheEnginePackagesOwn(t *testing.T) {
	d := Hosting()
	assert.Equal(t, engine.Name(claude.EngineName), d.Engine)
}

func TestHosting_EveryCapabilityClaudeCarriesIsProvided(t *testing.T) {
	d := Hosting()
	for name, decided := range map[string]bool{
		"SettingsWriter":  d.SettingsWriter.Decided() && d.SettingsWriter.AbsentReason() == "",
		"HookGlobalScope": d.HookGlobalScope.Decided() && d.HookGlobalScope.AbsentReason() == "",
		"VersionCommand":  d.VersionCommand.Decided() && d.VersionCommand.AbsentReason() == "",
	} {
		assert.True(t, decided, "%s must be provided for claude", name)
	}
	assert.Equal(t, claude.EngineName, d.NewBackend(nil).Name())
	assert.Equal(t, claude.EngineName, d.NewConfig().BackendType())
}

// Transcripts are the vendor reader's adapters as port values: one per
// declared version span, each reporting its own range.
func TestTranscripts_AreTheReadersAsPortValues(t *testing.T) {
	readers := Transcripts()
	require.NotEmpty(t, readers)
	for _, r := range readers {
		min, max := r.Versions()
		assert.NotEmpty(t, min)
		assert.NotEmpty(t, max)
	}
}

// parseVersion reads the MEASURED shape: "2.1.225 (Claude Code)".
func TestParseVersion_VersionLeadsNameFollows(t *testing.T) {
	v, err := parseVersion("2.1.225 (Claude Code)\n")
	require.NoError(t, err)
	assert.Equal(t, "2.1.225", v)
}
