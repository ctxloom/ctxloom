package config

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
)

// cloneProfile once omitted Profile.DenyTools, so every copy-on-read
// profile accessor handed back a slice that aliased the shared Config's
// storage. The hole was closed as a side finding of the fix to toDoc/ToFixture
// below, but the gate that found it walks Fixture, not the accessors named
// here — so nothing pinned the accessors themselves. These do.
//
// The contract under test is accessors.go's whole reason to exist: a value
// handed out by a Get* accessor is separately owned, and mutating it can never
// reach back into the Config every Load()/Current() holder shares.

// TestGetProfilesConfig_NeverAliasesConfigContainers is the class gate: it
// walks the returned value reflectively, so a Profile field added tomorrow and
// wired into cloneProfile without a clone helper fails HERE.
func TestGetProfilesConfig_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	got := cfg.GetProfilesConfig()

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(got), "Config", "GetProfilesConfig")
}

// TestGetProfileDefinitions_NeverAliasesConfigContainers covers the second
// accessor over the same storage; the two build their result differently
// enough that one being right does not make the other right.
func TestGetProfileDefinitions_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	got := cfg.GetProfileDefinitions()

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(got), "Config", "GetProfileDefinitions")
}

// TestGetProfileDefinitions_DenyToolsMutationDoesNotReachConfig states the
// instance F02 named, as behaviour rather than as pointer identity: a caller
// that appends to a returned deny-list must not be editing the shared config's
// deny-list. deny_tools gates which engine tools an agent may call, so an
// aliased slice is a permission surface mutated by action at a distance.
func TestGetProfileDefinitions_DenyToolsMutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())
	require.NotEmpty(t, cfg.GetProfileDefinitions()["p"].DenyTools,
		"the probe profile must carry a deny-list, or this pin proves nothing")

	got := cfg.GetProfileDefinitions()["p"]
	got.DenyTools[0] = "MUTATED"

	assert.Equal(t, []string{"Bash"}, cfg.GetProfileDefinitions()["p"].DenyTools,
		"GetProfileDefinitions must hand back an owned copy of DenyTools")
}

// toDoc copied the Config's maps and slices (agents,
// isolationImages, isolationEngines, lm.Configs, profiles.Definitions) BY
// REFERENCE, and MarshalYAML — which is exported and returns exactly that doc
// — is the one public path out of the type. The aliasing was closed by
// 91bf8f67, which fixed toDoc alongside its near-identical twin ToFixture, but
// the gate it shipped walks Fixture and never touched this seam. Pin it here
// so the doc projection cannot silently regress to sharing.

// TestMarshalYAML_NeverAliasesConfigContainers is the class gate on the
// exported marshal path: whatever yaml.Marshal(cfg) is handed must be
// independently owned, because a custom Marshaler's result is reachable by
// every caller of `config show`/`config get` and by the layer-remarshal step.
func TestMarshalYAML_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	doc, err := cfg.MarshalYAML()
	require.NoError(t, err)

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(doc), "Config", "MarshalYAML")
}

// TestMarshalYAML_MutationDoesNotReachConfig states the same contract as
// behaviour on the five containers F22 named by hand, so a future doc
// projection that reintroduces sharing for one of them fails with a message
// naming the field rather than a pointer.
func TestMarshalYAML_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	doc, err := cfg.MarshalYAML()
	require.NoError(t, err)
	d, ok := doc.(configDoc)
	require.True(t, ok, "MarshalYAML must still return a configDoc, or this pin is testing nothing")

	d.Agents["injected"] = agents.Agent{LLM: "fast"}
	d.Profiles.Definitions["injected"] = Profile{Description: "should not land"}
	d.IsolationImages["injected"] = "img"
	d.LM.Configs["injected"] = LLMConfig{Type: "claude-code"}

	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected")
	assert.NotContains(t, cfg.GetProfilesConfig().Definitions, "injected")
	assert.Empty(t, cfg.IsolationImageFor("injected"))
	assert.NotContains(t, cfg.GetLMConfig().Configs, "injected")
}
