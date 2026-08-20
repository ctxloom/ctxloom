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
	d.IsolationImages["injected"] = "img"
	d.LM.Configs["injected"] = LLMConfig{Type: "claude-code"}

	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected")
	assert.Empty(t, cfg.IsolationImageFor("injected"))
	assert.NotContains(t, cfg.GetLMConfig().Configs, "injected")
}
