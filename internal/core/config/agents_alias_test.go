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

// TestLoadAgents_MutationDoesNotReachConfig states it as behaviour, on the two
// fields F05 named.
func TestLoadAgents_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	for _, a := range cfg.LoadAgents() {
		require.NotEmpty(t, a.Profiles)
		require.NotEmpty(t, a.Escalation)
		a.Profiles[0] = "MUTATED"
		a.Escalation[0].Kinds[0] = "MUTATED"
		a.Escalation[0].Action = "MUTATED"
	}

	worker := cfg.GetConfiguredAgents()["worker"]
	assert.Equal(t, []string{"p"}, worker.Profiles,
		"LoadAgents must hand back an owned copy of Profiles")
	assert.Equal(t, []string{"TOOL_USE"}, worker.Escalation[0].Kinds,
		"LoadAgents must hand back an owned copy of each rung's Kinds")
	assert.Equal(t, "auto_accept", worker.Escalation[0].Action,
		"LoadAgents must hand back an owned copy of the Escalation slice itself")
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
