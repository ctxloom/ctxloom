package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineSettingsStatus tells "typo'd/unregistered name" apart from "a
// composed engine with nothing wired": an UNREGISTERED name errors, so a
// typo cannot read as a clean, empty, successful-looking status.
func TestEngineSettingsStatus_UnregisteredEngineErrors(t *testing.T) {
	_, err := engineSettingsStatus(engines.Registry(), "unknown-backend", "/project")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-backend")
}

// A composed engine with nothing installed at projectDir is a clean,
// error-free unwired read.
func TestEngineSettingsStatus_ComposedEngineWithNothingWiredIsUnwiredNoError(t *testing.T) {
	status, err := engineSettingsStatus(engines.Registry(), "mock", "/project")
	require.NoError(t, err)
	assert.False(t, status.Wired())
	assert.False(t, status.SettingsExists)
}
