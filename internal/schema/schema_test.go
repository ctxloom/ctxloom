package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
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
    c:   { type: codex }
  defaults:
    primary: big
    fast: big
default_agent: dev
agents:
  dev:
    llm: big
    profiles: [proj/dev]
config:
  use_distilled: true
  essence_max_chars: 8000
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

// TestConfigValidator_RejectsTypoedHookKey is the concrete
// regression: $defs/hook used to set additionalProperties:true, so a typo'd key
// inside a hook definition (e.g. "commnd" for "command") validated
// successfully with zero warning and the hook silently never fired. Now the
// hook object is closed, so a typo is a validation error.
func TestConfigValidator_RejectsTypoedHookKey(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	yaml := `
version: 6
hooks:
  unified:
    session_start:
      - commnd: echo hi
        typo_key: whatever
`
	err = v.ValidateBytes([]byte(yaml))
	assert.Error(t, err, "a typo'd hook key must be rejected, not silently accepted")
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

	// These are the two shapes yaml.v3 hands back that are NOT
	// already JSON-native — the only two conversions this function exists to
	// perform. Both used to fall through `default` untouched (verified:
	// reflect.DeepEqual(in, convertToJSON(in)) was true for every case here),
	// so the schema validator saw a bare Go-type panic-adjacent jsonType
	// error rather than a *jsonschema.ValidationError.
	t.Run("converts an unquoted YAML timestamp to an RFC3339 string", func(t *testing.T) {
		ts := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
		result := convertToJSON(ts)
		s, ok := result.(string)
		require.True(t, ok, "time.Time must convert to a JSON-representable string, got %T", result)
		assert.Equal(t, ts.Format(time.RFC3339), s)
	})

	t.Run("converts a non-string-keyed YAML map to a string-keyed one", func(t *testing.T) {
		// A bare (unquoted) map key like `1:` or `true:` decodes to this shape.
		input := map[interface{}]interface{}{
			1:    "one",
			true: "yes",
		}
		result := convertToJSON(input)
		m, ok := result.(map[string]interface{})
		require.True(t, ok, "map[interface{}]interface{} must convert to map[string]interface{}, got %T", result)
		assert.Equal(t, "one", m["1"])
		assert.Equal(t, "yes", m["true"])
	})

	t.Run("recurses into a non-string-keyed map's values", func(t *testing.T) {
		input := map[interface{}]interface{}{
			1: map[interface{}]interface{}{2: "nested"},
		}
		result := convertToJSON(input)
		m := result.(map[string]interface{})
		inner, ok := m["1"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "nested", inner["2"])
	})
}

// End-to-end through ValidateBytes (not just convertToJSON in
// isolation) — the shapes above must reach the validator as JSON-native
// values instead of surfacing a bare Go jsonType error that
// classifyValidationError's errors.As on *jsonschema.ValidationError cannot
// classify.
func TestValidateBytes_ConvertsYAMLTimestampAndNonStringKeys(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	t.Run("unquoted timestamp value no longer produces a bare jsonType error", func(t *testing.T) {
		// default_agent is a plain string field; an unquoted date is accepted
		// by YAML as a timestamp scalar. Before the fix this returned
		// `jsonschema: invalid jsonType: time.Time` — not a
		// *jsonschema.ValidationError` — regardless of whether the schema
		// would otherwise accept or reject the value.
		err := v.ValidateBytes([]byte("default_agent: 2026-07-24\n"))
		if err != nil {
			assert.NotContains(t, err.Error(), "invalid jsonType: time.Time",
				"a converted timestamp must be judged against the schema, not fail as an unrepresentable Go type")
		}
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

// TestConfigValidator_ConfigHome_UnknownValueRefused proves an unrecognized
// agents.<name>.config_home is a schema REFUSAL at load — not merely a
// message printed somewhere downstream — naming both the offending key and
// the two legal values. Distinct from operations.validateAgentAxes (the
// write-edge check on the SetAgent path): this is the boundary check on a
// hand-edited config.yaml, which the write-edge validator never sees.
func TestConfigValidator_ConfigHome_UnknownValueRefused(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	yaml := `
agents:
  dev:
    config_home: bogus
`
	err = v.ValidateBytes([]byte(yaml))
	require.Error(t, err, "an unknown config_home value must be refused at load")
	assert.Contains(t, err.Error(), "config_home")
	assert.Contains(t, err.Error(), agents.ConfigHomeProject)
	assert.Contains(t, err.Error(), agents.ConfigHomeHost)
}

// TestConfigValidator_ConfigHome_LegalValuesLoadCleanly is the control for
// the refusal test above: both real config_home values must validate with no
// error, so the refusal test isn't passing because the fixture never reaches
// the validator at all.
func TestConfigValidator_ConfigHome_LegalValuesLoadCleanly(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	names := agents.ConfigHomeNames()
	require.Len(t, names, 2, "fixture sanity: config_home has exactly two legal values")

	for _, name := range names {
		yaml := fmt.Sprintf("agents:\n  dev:\n    config_home: %s\n", name)
		err := v.ValidateBytes([]byte(yaml))
		assert.NoError(t, err, "config_home: %s must validate cleanly", name)
	}
}

// TestConfigSchema_ConfigHomeEnumMatchesGoNames binds the schema's
// agents.<name>.config_home enum to agents.ConfigHomeNames() so the two
// cannot silently drift apart — config-schema.json is hand-authored (no
// generator ties its enums to Go), so this is the drift gate in place of
// one. Add a member to ConfigHomeNames without updating the schema and this
// test fails.
func TestConfigSchema_ConfigHomeEnumMatchesGoNames(t *testing.T) {
	raw, err := resources.GetConfigSchema()
	require.NoError(t, err)

	var doc struct {
		Properties struct {
			Agents struct {
				AdditionalProperties struct {
					Properties struct {
						ConfigHome struct {
							Enum []string `json:"enum"`
						} `json:"config_home"`
					} `json:"properties"`
				} `json:"additionalProperties"`
			} `json:"agents"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	schemaEnum := doc.Properties.Agents.AdditionalProperties.Properties.ConfigHome.Enum
	require.NotEmpty(t, schemaEnum, "fixture sanity: config_home enum must be present in the schema at all")
	sort.Strings(schemaEnum)

	want := append([]string(nil), agents.ConfigHomeNames()...)
	sort.Strings(want)

	assert.Equal(t, want, schemaEnum, "schema config_home enum must match agents.ConfigHomeNames() exactly")
}
