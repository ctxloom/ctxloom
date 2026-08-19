//go:build arch

package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
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

// configYAMLFields returns the serializable top-level yaml field names of
// Config's persisted shape (those carrying a yaml tag; runtime-only fields
// like AppPaths have none). Reflects over configDoc, not Config: Config's own
// fields are unexported (Phase 3 of the config-manager rework) and invisible
// to reflection regardless of which package does the reflecting — configDoc
// is the exported-field mirror Config's own MarshalYAML/UnmarshalYAML round-
// trip through for exactly this reason, so it carries the identical tag set.
func configYAMLFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeFor[configDoc]()
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

func TestArch_ConfigSchema_CoversEveryConfigField(t *testing.T) {
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

func TestArch_ConfigSchema_NoUnknownTopLevelProperty(t *testing.T) {
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

// intentionalOpenSchemaMaps are object schemas that legitimately accept
// arbitrary keys (a user-named map, not a fixed struct's fields) — the key
// is the LAST path segment leading to the object (its own property name, or
// its parent's if reached via `additionalProperties`/`patternProperties`).
// A prior audit found exactly one UNINTENTIONAL escape ($defs/hook); this
// is the acknowledged list every other one must appear on to be exempted.
var intentionalOpenSchemaMaps = map[string]bool{
	"configs":          true, // llm.configs: user-named backend labels
	"agents":           true, // agents: user-named agent bindings
	"definitions":      true, // profiles.definitions: user-named profiles
	"isolation_images": true, // isolation_images: user-named image labels
	"env":              true, // *.env: arbitrary environment variable maps
	"servers":          true, // mcp.servers: user-named MCP server labels
	"plugins":          true, // hooks.plugins: user-named plugin labels
}

// walkSchemaObjects recursively visits every object schema in a decoded JSON
// Schema document (map[string]any nodes carrying a "properties" or
// "patternProperties" key), calling visit with the last path segment leading
// to it and the node itself.
func walkSchemaObjects(t *testing.T, key string, node any, visit func(key string, obj map[string]any)) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		if _, hasProps := v["properties"]; hasProps {
			visit(key, v)
		}
		if _, hasPatternProps := v["patternProperties"]; hasPatternProps {
			visit(key, v)
		}
		for k, child := range v {
			walkSchemaObjects(t, k, child, visit)
		}
	case []any:
		for _, child := range v {
			walkSchemaObjects(t, key, child, visit)
		}
	}
}

// TestArch_ConfigSchema_EveryObjectDeclaresAdditionalProperties is
// the class fix: the file's founding premise ("additionalProperties:false at
// every level, so an unknown key is already detected on load") was false for
// exactly one object — $defs/hook, which omitted the keyword (defaulting to
// permissive) and so silently accepted a typo'd hook key (e.g. `commnd` for
// `command`) with zero warning, the hook silently never firing. This walks
// every object schema in the document — not just the top level and the
// agent-binding object the two prior tests already cover — and asserts each
// one declares additionalProperties: either false, or an explicit
// sub-schema for one of the acknowledged intentional open maps.
func TestArch_ConfigSchema_EveryObjectDeclaresAdditionalProperties(t *testing.T) {
	raw, err := resources.GetConfigSchema()
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))

	checked := 0
	walkSchemaObjects(t, "", doc, func(key string, obj map[string]any) {
		checked++
		ap, present := obj["additionalProperties"]
		if !present {
			assert.Truef(t, intentionalOpenSchemaMaps[key],
				"object schema under %q has no additionalProperties keyword — it defaults to permissive, "+
					"so a typo'd key is silently accepted; set it to false, or add %q to "+
					"intentionalOpenSchemaMaps with a note if it is a deliberate open map", key, key)
			return
		}
		if b, ok := ap.(bool); ok && b {
			assert.Truef(t, intentionalOpenSchemaMaps[key],
				"object schema under %q has additionalProperties:true — a typo'd key is silently accepted; "+
					"set it to false, or add %q to intentionalOpenSchemaMaps with a note if it is a "+
					"deliberate open map", key, key)
		}
	})
	require.Greater(t, checked, 10, "the walk must actually traverse the document's object schemas")
}

// TestArch_ConfigSchema_AcceptsParserAcceptedNestedForms is the nested-drift gate:
// every snippet here is a form the PARSER deliberately accepts, so the schema
// must validate it too — otherwise each load emits a spurious validation
// warning. Two real regressions are pinned: the FragmentRef struct form
// ({name, priority} — FragmentRef.UnmarshalYAML) and an llm.configs entry
// without `type` (defaulted by LLMConfig.EffectiveType).
func TestArch_ConfigSchema_AcceptsParserAcceptedNestedForms(t *testing.T) {
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
			"top-level runtime default (agent axis, rootless)",
			"runtime: container-rootless\n",
		},
		{
			// Both ownership modes are asserted because the schema enumerates
			// them separately: a fixture naming only one would still pass while
			// the schema silently rejected the other, and no compiler sees a
			// value that lives in a YAML string.
			"top-level runtime default (agent axis, rootful)",
			"runtime: container-rootful\n",
		},
		{
			"agent-level runtime override (rootless)",
			"agents:\n  reviewer:\n    llm: fast\n    profiles: [review]\n    runtime: container-rootless\n",
		},
		{
			"agent-level runtime override (rootful)",
			"agents:\n  reviewer:\n    llm: fast\n    profiles: [review]\n    runtime: container-rootful\n",
		},
		{
			"llm config entry with permissions posture",
			"llm:\n  configs:\n    main:\n      type: codex\n      permissions: plan\n",
		},
		{
			"kiro llm config entry with its native fields",
			"llm:\n  configs:\n    k:\n      type: kiro\n      model: kiro-model\n      effort: high\n      agent: ctxloom\n      agent_engine: v2\n      permissions: plan\n",
		},
		{
			"agent-level permissions posture",
			"agents:\n  reviewer:\n    llm: fast\n    profiles: [review]\n    permissions: bypass\n",
		},
		{
			"per-backend isolation image overrides",
			"isolation_images:\n  kiro: registry.example.com/my-kiro:v2\n  claude-code: my-claude:latest\n",
		},
		{
			"user base containerfile for local agent-image builds",
			"isolation_base_containerfile: container/base.Containerfile\n",
		},
		{
			"terminal-ui section (viewer prefix key + surround toggle)",
			"ui:\n  prefix_key: ctrl-t\n  surround: false\n",
		},
		// These four are REAL regressions found by the unknown-key work: each key
		// is honored by the parser but was absent from the schema, so
		// additionalProperties:false rejected it — and a validation warning is a
		// FATAL finding in strict mode. A user of a shipped feature (publisher
		// signing, tag-selected fragments, curated commands, permission escalation)
		// was aborted at startup and told their own valid key was unknown.
		{
			"publisher-signing defaults (config.sign)",
			"config:\n  sign:\n    default: true\n    key: SHA256:abc\n",
		},
		{
			"profile select_tags (content-selecting tags)",
			"profiles:\n  definitions:\n    dev:\n      select_tags: [go, testing]\n",
		},
		{
			"profile curated command exports",
			"profiles:\n  definitions:\n    dev:\n      commands: [\"bundle#commands/review\"]\n",
		},
		{
			"agent escalation rungs",
			"agents:\n  reviewer:\n    llm: fast\n    escalation:\n      - kinds: [TOOL_USE]\n        action: auto_accept\n      - action: relay_to_role\n        role: parent\n        timeout: 5m\n",
		},
		{
			// `coordinator:` is deliberately absent: it was REMOVED, not
			// renamed, and is now refused at load (agents.RetiredCoordinatorKey),
			// so the parser no longer accepts this form and the schema is right
			// to reject it. Delegation privilege is decided by depth against
			// delegation.depth, not declared per binding.
			"agent driving binding",
			"agents:\n  coord:\n    llm: fast\n    profiles: [review]\n    driving: oneshot\n",
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

// TestArch_ConfigSchema_AgentBindingPermitsEveryAgentField is the one-level-deeper
// drift gate for agent bindings: agents.Agent is the concrete struct backing
// each entry under the top-level `agents` map, so every serializable Agent
// field must be permitted by the agent-binding
// object schema (properties.agents.additionalProperties) — with that object's
// own additionalProperties:false, a field the schema omits makes an otherwise-
// valid binding rejected with a spurious "unknown key" warning (exactly how
// `coordinator`/`driving` drifted: added to the struct + CLI but never to this
// schema). Mirrors bundles.TestFragmentSchema_LLMBlockPermitsEveryEngine's
// nested-reflection approach.
func TestArch_ConfigSchema_AgentBindingPermitsEveryAgentField(t *testing.T) {
	raw, err := resources.GetConfigSchema()
	require.NoError(t, err)

	var doc struct {
		Properties struct {
			Agents struct {
				AdditionalProperties struct {
					AdditionalProperties bool                       `json:"additionalProperties"`
					Properties           map[string]json.RawMessage `json:"properties"`
				} `json:"additionalProperties"`
			} `json:"agents"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.Properties.Agents.AdditionalProperties.AdditionalProperties,
		"the agent-binding object's additionalProperties must be false so an unknown key is rejected")

	rt := reflect.TypeFor[agents.Agent]()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		_, ok := doc.Properties.Agents.AdditionalProperties.Properties[name]
		assert.Truef(t, ok,
			"agents.Agent has a serializable %q field that the agent-binding schema does not permit; "+
				"add it to the agents.additionalProperties.properties object in "+
				"resources/schema/input/config-schema.json.", name)
	}
}

func TestArch_ConfigSchema_ShippedConfigsValidate(t *testing.T) {
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

// profileSchemaProperties returns the profile-object schema
// (profiles.definitions.<name>) and whether it rejects unknown keys.
func profileSchemaProperties(t *testing.T) (props map[string]json.RawMessage, additionalProps bool) {
	t.Helper()
	raw, err := resources.GetConfigSchema()
	require.NoError(t, err)
	var doc struct {
		Properties struct {
			Profiles struct {
				Properties struct {
					Definitions struct {
						AdditionalProperties struct {
							AdditionalProperties bool                       `json:"additionalProperties"`
							Properties           map[string]json.RawMessage `json:"properties"`
						} `json:"additionalProperties"`
					} `json:"definitions"`
				} `json:"properties"`
			} `json:"profiles"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	def := doc.Properties.Profiles.Properties.Definitions.AdditionalProperties
	return def.Properties, def.AdditionalProperties
}

// profileYAMLFields returns Profile's serializable yaml field names.
func profileYAMLFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeFor[Profile]()
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

// TestArch_ProfileSchema_CoversEveryProfileField closes the hole a prior gap
// came through: the drift gate above reflects configDoc and so covers the TOP LEVEL
// ONLY. `deny_tools` had yaml+mapstructure tags and was honoured all the way
// through mergeProfileValues → toProfile, yet appeared nowhere in the schema —
// and profiles.definitions.<name> is additionalProperties:false, so using the
// feature made `ctxloom run` abort in strict mode and print "it is IGNORED"
// (which was false) in degraded mode. A field the parser honours but the
// schema rejects is a feature that cannot be used.
func TestArch_ProfileSchema_CoversEveryProfileField(t *testing.T) {
	props, additional := profileSchemaProperties(t)
	require.NotEmpty(t, props, "the profile object schema must be reachable for this gate to mean anything")
	assert.False(t, additional,
		"profiles.definitions.<name>.additionalProperties must stay false so a typo'd profile key is still caught")

	for _, name := range profileYAMLFields(t) {
		_, ok := props[name]
		assert.Truef(t, ok,
			"Profile has a serializable %q field but the profile object in config-schema has no property for it — "+
				"a profile using it is rejected by additionalProperties:false and the run aborts in strict mode. "+
				"Add it to resources/schema/input/config-schema.json.", name)
	}
}
