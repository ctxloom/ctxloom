package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/agents"
)

// This file is the UNTAGGED half of the config-save completeness pair. Its
// class gates — TestArch_ConfigSave_PersistsEveryConfigDocField and
// TestArch_ConfigSave_PrunesUnsetSections — moved to
// config_save_completeness_arch_test.go behind `//go:build arch`, where
// `just test-arch` selects them as a discrete attributable group.
//
// What stayed here, and why, matters more than what left. TestUISurvivesSaveRoundTrip
// is NOT a class gate: it is the instance that prompted the class fix, ordinary
// regression coverage pinning ONE named field, and it must keep running in the
// default suite. fullyPopulatedFixture stayed because it is shared — fixture_alias_test.go,
// which carries no build tag, builds its alias probe on top of it, so moving it
// behind the tag would have broken the untagged build outright.

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
		Settings:                     SettingsConfig{EssenceMaxChars: 4096},
		Sync:                         SyncConfig{AutoSync: &autoSync},
		Agents:                       map[string]agents.Agent{"worker": {LLM: "fast"}},
		DefaultAgent:                 "worker",
		Workspace:                    "worktree",
		DirtyTreeHandler:             "commit",
		Runtime:                      "container",
		Permissions:                  "plan",
		Delegation:                   DelegationConfig{Concurrency: 7, Depth: 2},
		IsolationImages:              map[string]string{"claude-code": "example.invalid/img:tag"},
		IsolationBaseContainerfile:   "Containerfile.base",
		IsolationDevcontainerBase:    &devcontainerBase,
		IsolationDevcontainerService: "app",
		IsolationEngines:             []string{"claude-code"},
		UI:                           UIConfig{PrefixKey: "ctrl-]", Surround: &surround},
		SessionReapAge:               "45d",
		SessionPurgeAge:              "180d",
	}
}

// TestSessionReapAgeSurvivesSaveRoundTrip pins the one named field end to
// end through the documented API: written, marshalled, parsed back, and read
// through the accessor that applies the default — so a save that dropped it
// would surface as the default, not the value written.
func TestSessionReapAgeSurvivesSaveRoundTrip(t *testing.T) {
	cfg := NewFixture(Fixture{
		Version:        CurrentConfigVersion,
		SessionReapAge: "45d",
	})

	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, "45d", reloaded.SessionReapAge(),
		"session_reap_age was silently discarded on save")
}

// TestUISurvivesSaveRoundTrip is the instance that prompted the class fix:
// the field whose absence prompted it, asserted end to end through the
// documented API.
func TestUISurvivesSaveRoundTrip(t *testing.T) {
	surround := false
	cfg := NewFixture(Fixture{
		Version: CurrentConfigVersion,
		UI:      UIConfig{PrefixKey: "ctrl-b", Surround: &surround},
	})

	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var doc configDoc
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, "ctrl-b", doc.UI.PrefixKey, "the viewer prefix key was silently discarded on save")
	if assert.NotNil(t, doc.UI.Surround, "the surround toggle was silently discarded on save") {
		assert.False(t, *doc.UI.Surround, "an explicit surround:false must round-trip as false, not vanish into the default true")
	}
}

// TestDelegationDepthAloneSurvivesSaveRoundTrip covers the gap the class
// assertion above cannot: `delegation` is persisted as ONE key, guarded by a
// condition that names each of its fields, so a config in which one field is
// the ONLY thing set is pruned away entirely unless that condition names it.
// The fully-populated fixture always sets the others, so it can never catch
// it — this is the case that actually loses a user's setting.
func TestDelegationDepthAloneSurvivesSaveRoundTrip(t *testing.T) {
	cfg := NewFixture(Fixture{
		Version:    CurrentConfigVersion,
		Delegation: DelegationConfig{Depth: 2},
	})

	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var doc configDoc
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, 2, doc.Delegation.Depth,
		"delegation.depth was silently discarded on save")

	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, 2, reloaded.GetDelegationDepth(), "the accessor must read back what was written")
}

// session_purge_age has NO default: unset reads as empty, so a sweep
// reports its purge rows and acts on none of them.
func TestSessionPurgeAgeSurvivesSaveRoundTrip(t *testing.T) {
	data, err := yaml.Marshal(NewFixture(Fixture{Version: CurrentConfigVersion, SessionPurgeAge: "180d"}))
	require.NoError(t, err)
	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, "180d", reloaded.SessionPurgeAge())

	data, err = yaml.Marshal(NewFixture(Fixture{Version: CurrentConfigVersion}))
	require.NoError(t, err)
	reloaded, err = ParseConfig(data)
	require.NoError(t, err)
	assert.Empty(t, reloaded.SessionPurgeAge(), "unset has no default")
}
