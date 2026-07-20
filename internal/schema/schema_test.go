package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/resources"
)

func TestNewConfigValidator(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)
	assert.NotNil(t, v)
	assert.NotNil(t, v.schema)
}

func TestConfigValidator_ValidateBytes(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	t.Run("valid config", func(t *testing.T) {
		yaml := `
version: 6
llm:
  configs:
    big: { type: claude-code, model: opus }
    g:   { type: antigravity, model: gemini-3-pro }
  defaults:
    primary: big
    fast: big
default_agent: dev
agents:
  dev:
    engine: big
    profiles: [proj/dev]
profiles:
  definitions:
    proj/dev:
      tags: [go]
config:
  use_distilled: true
  compaction_chunks: 8000
`
		err := v.ValidateBytes([]byte(yaml))
		assert.NoError(t, err)
	})

	t.Run("empty config is valid", func(t *testing.T) {
		yaml := `{}`
		err := v.ValidateBytes([]byte(yaml))
		assert.NoError(t, err)
	})

	t.Run("invalid YAML", func(t *testing.T) {
		yaml := `invalid: yaml: [[`
		err := v.ValidateBytes([]byte(yaml))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "YAML parse error")
	})

	t.Run("embedded example config is valid", func(t *testing.T) {
		exampleConfig, err := resources.GetExampleConfig()
		require.NoError(t, err, "failed to read embedded example config")
		require.NotEmpty(t, exampleConfig, "example config should not be empty")

		err = v.ValidateBytes(exampleConfig)
		assert.NoError(t, err, "embedded example config should validate against schema")
	})
}

func TestConvertToJSON(t *testing.T) {
	t.Run("converts map", func(t *testing.T) {
		input := map[string]interface{}{
			"key1": "value1",
			"key2": map[string]interface{}{
				"nested": "value",
			},
		}
		result := convertToJSON(input)
		m, ok := result.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "value1", m["key1"])

		nested, ok := m["key2"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "value", nested["nested"])
	})

	t.Run("converts array", func(t *testing.T) {
		input := []interface{}{"a", "b", "c"}
		result := convertToJSON(input)
		arr, ok := result.([]interface{})
		require.True(t, ok)
		assert.Len(t, arr, 3)
		assert.Equal(t, "a", arr[0])
	})

	t.Run("passes through primitives", func(t *testing.T) {
		assert.Equal(t, "string", convertToJSON("string"))
		assert.Equal(t, 42, convertToJSON(42))
		assert.Equal(t, true, convertToJSON(true))
		assert.Nil(t, convertToJSON(nil))
	})

	t.Run("handles nested arrays in maps", func(t *testing.T) {
		input := map[string]interface{}{
			"tags": []interface{}{"tag1", "tag2"},
		}
		result := convertToJSON(input)
		m, ok := result.(map[string]interface{})
		require.True(t, ok)

		tags, ok := m["tags"].([]interface{})
		require.True(t, ok)
		assert.Equal(t, "tag1", tags[0])
		assert.Equal(t, "tag2", tags[1])
	})
}

// TestConfigValidator_KnownPath exercises KnownPath -- the seam confload's
// env/CLI override resolution uses to tell a legitimate-but-unset schema key
// (case 3, created silently) apart from a genuinely unrecognized one (case 4,
// warned about). See confload's resolvePath doc for the full algorithm.
func TestConfigValidator_KnownPath(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	t.Run("top-level scalar property", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"default_agent"}))
	})

	t.Run("nested property under a fixed object", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"llm", "defaults", "primary"}))
	})

	t.Run("dynamic map key (agent label) then its own property", func(t *testing.T) {
		// "agents" is additionalProperties: {object with `runtime` etc.} --
		// ANY label at that level must be accepted (it's a user-chosen agent
		// name the schema cannot enumerate), and `runtime` must resolve as a
		// property of the per-agent schema underneath it.
		assert.True(t, v.KnownPath([]string{"agents", "some_never_before_seen_label", "runtime"}))
	})

	t.Run("unknown top-level key", func(t *testing.T) {
		assert.False(t, v.KnownPath([]string{"totally_bogus_top_level_key"}))
	})

	t.Run("unknown nested key under a known fixed object", func(t *testing.T) {
		assert.False(t, v.KnownPath([]string{"llm", "defaults", "bogus"}))
	})

	t.Run("empty path is never known", func(t *testing.T) {
		assert.False(t, v.KnownPath(nil))
	})

	t.Run("nil validator degrades to false, never panics", func(t *testing.T) {
		var nilValidator *ConfigValidator
		assert.False(t, nilValidator.KnownPath([]string{"default_agent"}))
	})
}
