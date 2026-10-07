package wire

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
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

// TestHooksConfig_UnmarshalYAML_IsStrict: a hooks block decodes strictly at
// every level, whatever document embeds it. A type's own UnmarshalYAML does
// not inherit its caller's KnownFields, so without this an unknown key — a
// misspelled event, a key that once meant something — would decode into a
// config with those hooks silently missing.
func TestHooksConfig_UnmarshalYAML_IsStrict(t *testing.T) {
	for name, doc := range map[string]string{
		"top level": "unified:\n  pre_tool:\n    - command: u\nplugins:\n  claude-code: {}\n",
		"an event":  "unified:\n  pre_tooll:\n    - command: u\n",
		"a hook":    "unified:\n  pre_tool:\n    - comand: u\n",
	} {
		t.Run(name, func(t *testing.T) {
			var h HooksConfig
			err := yaml.Unmarshal([]byte(doc), &h)
			require.Error(t, err)
			assert.Regexp(t, `plugins|pre_tooll|comand`, err.Error(), "the refusal names the key")
		})
	}
}

// TestHooksConfig_UnmarshalYAML_ExtEngineNamesAreFree: under ext the engine
// labels and native event names are the author's, not keys the type models.
func TestHooksConfig_UnmarshalYAML_ExtEngineNamesAreFree(t *testing.T) {
	const doc = "ext:\n  plugins:\n    SomeEvent:\n      - command: x\n        matcher: plugins\n"

	var h HooksConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &h))
	require.Len(t, h.Ext["plugins"]["SomeEvent"], 1)
}

// TestHooksConfig_MarshalYAML_WritesExt pins the write side: what Save emits
// is what UnmarshalYAML reads back, under the one current spelling.
func TestHooksConfig_MarshalYAML_WritesExt(t *testing.T) {
	h := HooksConfig{Ext: map[string]BackendHooks{"claude-code": {"PreToolUse": []Hook{{Command: "x"}}}}}
	out, err := yamlx.Marshal(h)
	require.NoError(t, err)
	assert.Contains(t, string(out), "ext:")
	assert.NotContains(t, string(out), "plugins")

	var back HooksConfig
	require.NoError(t, yaml.Unmarshal(out, &back))
	assert.Equal(t, h, back)
}
