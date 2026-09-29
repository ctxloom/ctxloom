package config

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadAgents_NeverAliasesConfigContainers is the class gate — a slice field
// added to agents.Agent tomorrow and not cloned fails here.
func TestLoadAgents_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	list := cfg.LoadAgents()
	require.NotEmpty(t, list, "the probe fixture must define an agent, or this gate proves nothing")

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(list), "Config", "LoadAgents")
}

// TestLoadAgents_MutationDoesNotReachConfig states it as behaviour.
func TestLoadAgents_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	for _, a := range cfg.LoadAgents() {
		require.NotEmpty(t, a.Profiles)
		a.Profiles[0] = "MUTATED"
	}

	worker := cfg.GetConfiguredAgents()["worker"]
	assert.Equal(t, []string{"p"}, worker.Profiles,
		"LoadAgents must hand back an owned copy of Profiles")
}

// TestAgent_MutationDoesNotReachConfig covers the single-name lookup, which is
// the path operations.ResolveAgent and DefaultAgentProfiles actually take.
func TestAgent_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	got, ok := cfg.Agent("worker")
	require.True(t, ok)
	require.NotEmpty(t, got.Profiles)
	got.Profiles[0] = "MUTATED"

	assert.Equal(t, []string{"p"}, cfg.GetConfiguredAgents()["worker"].Profiles,
		"Agent must hand back an owned copy of Profiles")
}
