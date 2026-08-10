//go:build arch

package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Every persisted config field is declared in FOUR hand-maintained places —
// Config, configDoc, Fixture and applyConfigSections — and the fourth is the
// one that gets forgotten. applyConfigSections' own
// comments document that happening twice already (`dirty_tree_handler` /
// `dirty_tree_commit_ack`, then `agent_turn_cap`): each time, a write through
// the documented Draft/Fixture API compiled, reported success, and was
// silently discarded. `ui` was the third.
//
// This is the CLASS fix rather than the instance fix: configDoc is the
// declaration of what "persisted" means, so any yaml tag on it that
// applyConfigSections does not emit is the same bug again, caught here rather
// than in the field.
//
// The class gates live HERE, behind `//go:build arch`, so `just test-arch` can
// select them as a discrete attributable group. The INSTANCE test they were
// distilled from (TestUISurvivesSaveRoundTrip) deliberately stayed behind in
// config_save_completeness_test.go, untagged: it is ordinary regression
// coverage for one named field, not a class gate, and tagging it would have
// quietly removed it from the default suite. So did fullyPopulatedFixture,
// which fixture_alias_test.go — untagged — also builds on.

// persistedYAMLKeys is configDoc's declaration of the persisted surface: the
// yaml tag of every field on it.
func persistedYAMLKeys(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(configDoc{})
	keys := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, strings.Split(tag, ",")[0])
	}
	require.NotEmpty(t, keys, "configDoc must declare yaml tags for this test to mean anything")
	return keys
}

// TestArch_ConfigSave_PersistsEveryConfigDocField is the class assertion. It is
// deliberately reflective: a new persisted field added to configDoc and
// Fixture but forgotten in applyConfigSections fails HERE, at the moment it is
// added, instead of silently dropping users' settings.
func TestArch_ConfigSave_PersistsEveryConfigDocField(t *testing.T) {
	cfg := NewFixture(fullyPopulatedFixture())

	data, err := cfg.Marshal()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))

	for _, key := range persistedYAMLKeys(t) {
		assert.Contains(t, got, key,
			"configDoc declares %q as persisted, but a Marshal() of a fully-populated Fixture does not emit it: "+
				"applyConfigSections is missing a setOrDelete for it, so every write of this field through "+
				"Save()/Marshal() is silently discarded", key)
	}
}

// TestArch_ConfigSave_PrunesUnsetSections keeps the completeness assertion honest: it
// must not be satisfiable by emitting every key unconditionally. An unset
// section is still pruned, so an empty config stays empty on disk.
func TestArch_ConfigSave_PrunesUnsetSections(t *testing.T) {
	cfg := NewFixture(Fixture{Version: CurrentConfigVersion})

	data, err := cfg.Marshal()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))

	for _, key := range []string{"ui", "agents", "default_agent", "workspace", "runtime", "delegation"} {
		assert.NotContains(t, got, key, "an unset %q must be pruned, not written as an empty block", key)
	}
}
