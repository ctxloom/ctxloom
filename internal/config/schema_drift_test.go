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

// schemaOnlyTopLevel are top-level schema properties intentionally without a
// matching Config field, acknowledged so a NEW orphan is still caught. Empty
// today: the schema and the struct are in exact correspondence.
var schemaOnlyTopLevel = map[string]bool{}

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

// TestConfigSchema_AcceptsParserAcceptedNestedForms is the nested-drift gate:
// every snippet here is a form the PARSER deliberately accepts, so the schema
// must validate it too — otherwise each load emits a spurious validation
// warning. Two real regressions are pinned: the FragmentRef struct form
// ({name, priority} — FragmentRef.UnmarshalYAML) and an llm.configs entry
// without `type` (defaulted by LLMConfig.EffectiveType).
func TestConfigSchema_AcceptsParserAcceptedNestedForms(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err)

	for _, c := range []struct {
		name string
		yaml string
	}{
		{
			"fragment struct form with priority",
			"profiles:\n  definitions:\n    dev:\n      fragments:\n        - go-style\n        - name: testing\n          priority: 10\n",
		},
		{
			"llm config entry without type",
			"llm:\n  configs:\n    main:\n      model: claude-opus-4-8\n",
		},
		{
			"llm config entry with explicit type",
			"llm:\n  configs:\n    main:\n      type: codex\n      model: gpt-codex\n",
		},
		{
			"top-level workspace default (session axis)",
			"workspace: worktree\n",
		},
		{
			"top-level runtime default (agent axis)",
			"runtime: container\n",
		},
		{
			"agent-level runtime override",
			"agents:\n  reviewer:\n    engine: fast\n    profiles: [review]\n    runtime: container\n",
		},
		{
			"per-backend isolation image overrides",
			"isolation_images:\n  kiro: registry.example.com/my-kiro:v2\n  claude-code: my-claude:latest\n",
		},
		{
			"user base containerfile for local agent-image builds",
			"isolation_base_containerfile: container/base.Containerfile\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The parser accepts it...
			cfg, perr := ParseConfig([]byte(c.yaml))
			require.NoError(t, perr)
			require.NotNil(t, cfg)
			// ...so the schema must too.
			assert.NoError(t, v.ValidateBytes([]byte(c.yaml)),
				"the parser accepts this form, so validating it must not warn")
		})
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
