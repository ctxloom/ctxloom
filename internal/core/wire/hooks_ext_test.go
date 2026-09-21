package wire

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestHooksConfig_UnmarshalYAML_ExtKeyReadsEngineHooks pins the on-disk
// spelling of the engine-namespaced passthrough map: `ext:` (engine name →
// native event → hooks), alongside `unified:`.
func TestHooksConfig_UnmarshalYAML_ExtKeyReadsEngineHooks(t *testing.T) {
	const doc = "unified:\n  pre_tool:\n    - command: u\n" +
		"ext:\n  claude-code:\n    PreToolUse:\n      - command: x\n"

	var h HooksConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &h))

	require.Len(t, h.Unified.PreTool, 1, "the unified half still decodes beside ext")
	assert.Equal(t, "u", h.Unified.PreTool[0].Command)
	require.Contains(t, h.Ext, "claude-code")
	require.Len(t, h.Ext["claude-code"]["PreToolUse"], 1)
	assert.Equal(t, "x", h.Ext["claude-code"]["PreToolUse"][0].Command)
}

// TestHooksConfig_UnmarshalYAML_RetiredPluginsKeyRefused pins the rename's
// loud failure. yaml.v3 without KnownFields drops a key it cannot map, so a
// hooks block still spelling the retired `plugins:` would decode into a
// config with NO engine-specific hooks and every signal green — the silent
// no-op this codebase treats as its characteristic bug. The refusal is a
// sentinel (errors.Is-able) and its text names the current spelling, so the
// person reading it knows what to write instead.
func TestHooksConfig_UnmarshalYAML_RetiredPluginsKeyRefused(t *testing.T) {
	const doc = "unified:\n  pre_tool:\n    - command: u\n" +
		"plugins:\n  claude-code:\n    PreToolUse:\n      - command: x\n"

	var h HooksConfig
	err := yaml.Unmarshal([]byte(doc), &h)
	require.ErrorIs(t, err, ErrRetiredHooksExtKey)
	assert.Contains(t, err.Error(), "'"+RetiredHooksExtKey+":'", "the refusal names the retired key")
	assert.Contains(t, err.Error(), "'ext:'", "the refusal names the current spelling, not only the rejected one")
	assert.Empty(t, h.Ext, "a refused document must not half-decode")
}

// TestHooksConfig_UnmarshalYAML_PluginsAsEngineNameIsNotTheRetiredKey keeps
// the guard a KEY check: the word appearing one level down — as an engine
// label under ext, or as a matcher — is not the retired hooks key.
func TestHooksConfig_UnmarshalYAML_PluginsAsEngineNameIsNotTheRetiredKey(t *testing.T) {
	const doc = "ext:\n  plugins:\n    SomeEvent:\n      - command: x\n        matcher: plugins\n"

	var h HooksConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &h))
	require.Len(t, h.Ext["plugins"]["SomeEvent"], 1)
}

// TestHooksConfig_MarshalYAML_WritesExt pins the write side: what Save emits
// is what UnmarshalYAML reads back, under the one current spelling.
func TestHooksConfig_MarshalYAML_WritesExt(t *testing.T) {
	h := HooksConfig{Ext: map[string]BackendHooks{"claude-code": {"PreToolUse": []Hook{{Command: "x"}}}}}
	out, err := yaml.Marshal(h)
	require.NoError(t, err)
	assert.Contains(t, string(out), "ext:")
	assert.NotContains(t, string(out), "plugins")

	var back HooksConfig
	require.NoError(t, yaml.Unmarshal(out, &back))
	assert.Equal(t, h, back)
}
