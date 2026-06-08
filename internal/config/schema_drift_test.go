package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/resources"
)

// config-schema.json is HAND-AUTHORED (it validates user-written config.yaml, so
// the schema — not a struct — is the source of truth, carrying enums/patterns/
// prose reflection can't derive). It still must not drift from the Config struct
// that parses the validated YAML: a serializable field the struct accepts but
// the schema omits is rejected by additionalProperties:false (this is exactly
// how `sync` silently broke). These tests are that drift gate for the input
// schema — the authored-input counterpart to the generated output-schema check.

// schemaOnlyTopLevel are top-level schema properties with no matching Config
// field, acknowledged so a NEW orphan is still caught. "memory" is a vestigial
// block (it references the removed "plugin" terminology and nothing in Go reads
// a `memory:` section); remove it from the schema and from here together.
var schemaOnlyTopLevel = map[string]bool{"memory": true}

func configSchemaProperties(t *testing.T) (props map[string]json.RawMessage, additionalProps bool) {
	t.Helper()
	raw, err := resources.GetConfigSchema()
	require.NoError(t, err)
	var doc struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc.Properties, doc.AdditionalProperties
}

// configYAMLFields returns the serializable top-level yaml field names of Config
// (those carrying a yaml tag; runtime-only fields like AppPaths have none).
func configYAMLFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeFor[Config]()
	var names []string
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func TestConfigSchema_CoversEveryConfigField(t *testing.T) {
	props, additional := configSchemaProperties(t)
	assert.False(t, additional,
		"config-schema top-level additionalProperties must be false so an unknown section is rejected")

	for _, name := range configYAMLFields(t) {
		_, ok := props[name]
		assert.Truef(t, ok,
			"Config has a serializable %q field but config-schema has no top-level property for it — "+
				"a config with that section would be rejected by additionalProperties:false. Add it to "+
				"resources/schema/input/config-schema.json.", name)
	}
}

func TestConfigSchema_NoUnknownTopLevelProperty(t *testing.T) {
	props, _ := configSchemaProperties(t)
	fields := map[string]bool{}
	for _, n := range configYAMLFields(t) {
		fields[n] = true
	}
	for name := range props {
		if schemaOnlyTopLevel[name] {
			continue
		}
		assert.Truef(t, fields[name],
			"config-schema declares top-level property %q with no matching Config field; either add the "+
				"field or, if intentionally schema-only, add it to schemaOnlyTopLevel with a note.", name)
	}
}

func TestConfigSchema_ShippedConfigsValidate(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err)

	// Only the user-facing templates are validated. default-config.yaml is the
	// internal registry seed: it carries a registry-only `role` field (stripped
	// from any persisted user config), so it intentionally does NOT conform to
	// the user-input schema.
	for _, c := range []struct {
		name string
		load func() ([]byte, error)
	}{
		{"example-config", resources.GetExampleConfig},
		{"init-config", resources.GetInitConfig},
	} {
		t.Run(c.name, func(t *testing.T) {
			data, err := c.load()
			require.NoError(t, err)
			assert.NoError(t, v.ValidateBytes(data),
				"%s.yaml must validate against the config schema", c.name)
		})
	}
}
