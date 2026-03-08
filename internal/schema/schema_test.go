package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SophisticatedContextManager/scm/resources"
)

func TestNewValidator(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)
	assert.NotNil(t, v)
	assert.NotNil(t, v.schema)
}

func TestNewConfigValidator(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)
	assert.NotNil(t, v)
	assert.NotNil(t, v.schema)
}

func TestValidator_ValidateBytes(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	t.Run("valid fragment", func(t *testing.T) {
		yaml := `
content: |
  This is valid content.
tags:
  - test
`
		err := v.ValidateBytes([]byte(yaml))
		assert.NoError(t, err)
	})

	t.Run("minimal valid fragment", func(t *testing.T) {
		yaml := `content: "Simple content"`
		err := v.ValidateBytes([]byte(yaml))
		assert.NoError(t, err)
	})

	t.Run("invalid YAML", func(t *testing.T) {
		yaml := `invalid: yaml: [[`
		err := v.ValidateBytes([]byte(yaml))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "YAML parse error")
	})
}

func TestConfigValidator_ValidateBytes(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	t.Run("valid config", func(t *testing.T) {
		yaml := `
llm:
  plugins:
    claude-code: {}
defaults:
  llm_plugin: claude-code
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
