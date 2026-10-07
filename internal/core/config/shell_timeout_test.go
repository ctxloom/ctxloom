package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestShellTimeout_Unset_DefaultsToTenMinutesAndAnHour: a config that says
// nothing gets the built-in foreground default and ceiling, resolved here so
// no engine re-derives them.
func TestShellTimeout_Unset_DefaultsToTenMinutesAndAnHour(t *testing.T) {
	cfg, err := ParseConfig([]byte("schema_version: 7\n"))
	require.NoError(t, err)
	assert.Equal(t, engine.ShellTimeout{Default: 10 * time.Minute, Max: time.Hour}, cfg.GetShellTimeout())
}

// TestShellTimeout_Set_IsReadBackResolved: each half is a Go duration, and a
// half left unset keeps its own default.
func TestShellTimeout_Set_IsReadBackResolved(t *testing.T) {
	cfg, err := ParseConfig([]byte("schema_version: 7\nshell_timeout:\n  default: 5m\n  max: 2h\n"))
	require.NoError(t, err)
	assert.Equal(t, engine.ShellTimeout{Default: 5 * time.Minute, Max: 2 * time.Hour}, cfg.GetShellTimeout())

	cfg, err = ParseConfig([]byte("schema_version: 7\nshell_timeout:\n  default: 20m\n"))
	require.NoError(t, err)
	assert.Equal(t, engine.ShellTimeout{Default: 20 * time.Minute, Max: DefaultShellTimeoutMax}, cfg.GetShellTimeout())
}

// TestShellTimeout_Invalid_IsRefusedAtLoad: a half that does not parse or is
// not positive, or a default the resolved ceiling would not admit, is
// refused at load — never silently replaced by a default.
func TestShellTimeout_Invalid_IsRefusedAtLoad(t *testing.T) {
	for _, bad := range []string{
		"shell_timeout:\n  default: ten\n",
		"shell_timeout:\n  max: 0s\n",
		"shell_timeout:\n  default: -1m\n",
		"shell_timeout:\n  default: 2h\n",
		"shell_timeout:\n  default: 5m\n  max: 1m\n",
	} {
		_, err := ParseConfig([]byte("schema_version: 7\n" + bad))
		assert.ErrorIs(t, err, ErrInvalidShellTimeout, "%q", bad)
	}
}

// TestShellTimeout_SurvivesSaveRoundTrip pins the key through the documented
// API: written, marshalled, parsed back, read via the accessor.
func TestShellTimeout_SurvivesSaveRoundTrip(t *testing.T) {
	cfg := NewFixture(Fixture{SchemaVersion: CurrentConfigVersion, ShellTimeout: ShellTimeoutConfig{Default: "3m", Max: "90m"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, engine.ShellTimeout{Default: 3 * time.Minute, Max: 90 * time.Minute}, reloaded.GetShellTimeout())
}
