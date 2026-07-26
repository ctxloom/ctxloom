package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// U049-F03 / U049-F20: every persisted config field is declared in FOUR
// hand-maintained places — Config, configDoc, Fixture and applyConfigSections
// — and the fourth is the one that gets forgotten. applyConfigSections' own
// comments document that happening twice already (`dirty_tree_handler` /
// `dirty_tree_commit_ack`, then `agent_turn_cap`): each time, a write through
// the documented Draft/Fixture API compiled, reported success, and was
// silently discarded. `ui` was the third.
//
// This is the CLASS fix rather than the instance fix: configDoc is the
// declaration of what "persisted" means, so any yaml tag on it that
// applyConfigSections does not emit is the same bug again, caught here rather
// than in the field.

// fullyPopulatedFixture sets every PERSISTED field to a distinctly non-zero
// value, so a key missing from a Marshal() can only mean the persist path
// dropped it — never that the value was empty and legitimately pruned.
func fullyPopulatedFixture() Fixture {
	surround := true
	autoSync := true
	devcontainerBase := true
	return Fixture{
		Version:                      CurrentConfigVersion,
		LM:                           LMConfig{Configs: map[string]LLMConfig{"fast": {Type: "claude-code"}}},
		Editor:                       EditorConfig{Command: "vi"},
		Settings:                     SettingsConfig{CompactionChunks: 4096},
		Sync:                         SyncConfig{AutoSync: &autoSync},
		Hooks:                        wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "true"}}}},
		MCP:                          wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "true"}}},
		Profiles:                     ProfilesConfig{Definitions: map[string]Profile{"p": {Description: "a profile"}}},
		Agents:                       map[string]agents.Agent{"worker": {Engine: "fast"}},
		DefaultAgent:                 "worker",
		Workspace:                    "worktree",
		DirtyTreeHandler:             "commit",
		DirtyTreeCommitAck:           true,
		Runtime:                      "container",
		AgentTurnCap:                 7,
		IsolationImages:              map[string]string{"claude-code": "example.invalid/img:tag"},
		IsolationBaseContainerfile:   "Containerfile.base",
		IsolationDevcontainerBase:    &devcontainerBase,
		IsolationDevcontainerService: "app",
		IsolationEngines:             []string{"claude-code"},
		UI:                           UIConfig{PrefixKey: "ctrl-]", Surround: &surround},
	}
}

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

// TestMarshal_PersistsEveryConfigDocField is the class assertion. It is
// deliberately reflective: a new persisted field added to configDoc and
// Fixture but forgotten in applyConfigSections fails HERE, at the moment it is
// added, instead of silently dropping users' settings.
func TestMarshal_PersistsEveryConfigDocField(t *testing.T) {
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

// TestMarshal_PrunesUnsetSections keeps the completeness assertion honest: it
// must not be satisfiable by emitting every key unconditionally. An unset
// section is still pruned, so an empty config stays empty on disk.
func TestMarshal_PrunesUnsetSections(t *testing.T) {
	cfg := NewFixture(Fixture{Version: CurrentConfigVersion})

	data, err := cfg.Marshal()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))

	for _, key := range []string{"ui", "agents", "default_agent", "workspace", "runtime", "agent_turn_cap"} {
		assert.NotContains(t, got, key, "an unset %q must be pruned, not written as an empty block", key)
	}
}

// TestUISurvivesSaveRoundTrip is U049-F03's instance: the field whose absence
// prompted the class fix, asserted end to end through the documented API.
func TestUISurvivesSaveRoundTrip(t *testing.T) {
	surround := false
	cfg := NewFixture(Fixture{
		Version: CurrentConfigVersion,
		UI:      UIConfig{PrefixKey: "ctrl-b", Surround: &surround},
	})

	data, err := cfg.Marshal()
	require.NoError(t, err)

	var doc configDoc
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, "ctrl-b", doc.UI.PrefixKey, "the viewer prefix key was silently discarded on save")
	if assert.NotNil(t, doc.UI.Surround, "the surround toggle was silently discarded on save") {
		assert.False(t, *doc.UI.Surround, "an explicit surround:false must round-trip as false, not vanish into the default true")
	}
}
