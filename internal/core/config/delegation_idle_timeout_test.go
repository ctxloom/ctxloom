package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestDelegationIdleTimeout_Unset_DefaultsToFifteenMinutes: the idle reaper
// (coord) ends a runner that has had no turn for delegation.idle_timeout;
// a config that says nothing gets the built-in default, resolved HERE so
// the coordinator never re-derives it.
func TestDelegationIdleTimeout_Unset_DefaultsToFifteenMinutes(t *testing.T) {
	cfg, err := ParseConfig([]byte("version: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, 15*time.Minute, cfg.GetDelegationIdleTimeout())
	assert.Equal(t, DefaultDelegationIdleTimeout, cfg.GetDelegationIdleTimeout())
}

// TestDelegationIdleTimeout_Set_IsReadBackResolved: the key is a duration in
// Go's grammar, read through the accessor as a time.Duration.
func TestDelegationIdleTimeout_Set_IsReadBackResolved(t *testing.T) {
	cfg, err := ParseConfig([]byte("version: 1\ndelegation:\n  idle_timeout: 90s\n"))
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, cfg.GetDelegationIdleTimeout())
}

// TestDelegationIdleTimeout_Invalid_IsRefusedAtLoad: a value the grammar
// cannot parse, or one that is not positive, is refused at load — never
// silently replaced by the default, which is how a typo would turn into a
// reaper that fires at a cadence nobody configured.
func TestDelegationIdleTimeout_Invalid_IsRefusedAtLoad(t *testing.T) {
	for _, bad := range []string{"fifteen", "0s", "-5m"} {
		_, err := ParseConfig([]byte("version: 1\ndelegation:\n  idle_timeout: " + bad + "\n"))
		require.Error(t, err, "idle_timeout %q must be refused", bad)
		assert.ErrorIs(t, err, ErrInvalidIdleTimeout, "idle_timeout %q", bad)
	}
}

// TestDelegationIdleTimeout_SurvivesSaveRoundTrip pins the key through the
// documented API: written, marshalled, parsed back, read via the accessor.
func TestDelegationIdleTimeout_SurvivesSaveRoundTrip(t *testing.T) {
	cfg := NewFixture(Fixture{Version: CurrentConfigVersion, Delegation: DelegationConfig{IdleTimeout: "2h"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, reloaded.GetDelegationIdleTimeout())
}
