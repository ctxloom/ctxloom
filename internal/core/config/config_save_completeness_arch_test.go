//go:build arch

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Every write and every `config show` renders persistedDoc, which starts from
// toDoc. A persisted field declared on configDoc but not copied by toDoc
// renders as its zero value and is pruned, so a write through the documented
// Draft/Fixture API compiles, reports success, and is silently discarded —
// this project's characteristic failure. configDoc is the declaration of what
// "persisted" means, so any yaml tag on it that a render of a fully-populated
// Fixture does not emit is that bug, caught here rather than in the field.
//
// The class gates live HERE, behind `//go:build arch`, so `just test-arch` can
// select them as a discrete attributable group. The INSTANCE test they were
// distilled from (TestUISurvivesSaveRoundTrip) deliberately stayed behind in
// config_save_completeness_test.go, untagged: it is ordinary regression
// coverage for one named field, not a class gate, and tagging it would have
// quietly removed it from the default suite. So did fullyPopulatedFixture,
// which fixture_alias_test.go — untagged — also builds on.

// TestArch_ConfigSave_PersistsEveryConfigDocField is the class assertion. It is
// deliberately reflective: a new persisted field added to configDoc and
// Fixture but forgotten in toDoc fails HERE, at the moment it is
// added, instead of silently dropping users' settings.
func TestArch_ConfigSave_PersistsEveryConfigDocField(t *testing.T) {
	cfg := NewFixture(fullyPopulatedFixture())

	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))

	keys := persistedKeys()
	require.NotEmpty(t, keys, "configDoc must declare yaml tags for this test to mean anything")
	for _, key := range keys {
		assert.Contains(t, got, key,
			"configDoc declares %q as persisted, but a render of a fully-populated Fixture does not emit it: "+
				"toDoc does not copy it, so every write of this field is silently discarded", key)
	}
}

// TestArch_ConfigSave_PrunesUnsetSections keeps the completeness assertion honest: it
// must not be satisfiable by emitting every key unconditionally. An unset
// section is still pruned, so an empty config stays empty on disk.
func TestArch_ConfigSave_PrunesUnsetSections(t *testing.T) {
	cfg := NewFixture(Fixture{Version: CurrentConfigVersion})

	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))

	for _, key := range []string{"ui", "agents", "default_agent", "workspace", "runtime", "permissions", "delegation"} {
		assert.NotContains(t, got, key, "an unset %q must be pruned, not written as an empty block", key)
	}
}
