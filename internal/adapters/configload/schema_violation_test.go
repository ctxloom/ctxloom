package configload

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// fixtureConfigPath is where loadYAML writes its document; every violation
// must name it, because a refusal that does not say WHICH file is broken
// leaves the user to search both layers.
const fixtureConfigPath = "/proj/.ctxloom/config.yaml"

// schemaViolations returns the typed violation behind each schema-violation
// warning, failing the test if a warning of that kind carries no typed cause
// (the refusal gate and the docs both promise one).
func schemaViolations(t *testing.T, cfg *config.Config) []*config.SchemaViolation {
	t.Helper()
	var out []*config.SchemaViolation
	for _, w := range cfg.GetWarnings() {
		if w.Kind != config.WarnKindSchemaViolation {
			continue
		}
		var v *config.SchemaViolation
		require.True(t, errors.As(w.Cause, &v), "a %s warning must carry a *config.SchemaViolation cause; got %#v", w.Kind, w.Cause)
		require.ErrorIs(t, w.Cause, config.ErrSchemaViolation)
		assert.Equal(t, v.Error(), w.Text, "the warning text is the violation's own rendering")
		out = append(out, v)
	}
	return out
}

// A value of the wrong type is a schema violation named by file, dotted key
// path and the type the schema expected — not the raw jsonschema pointer dump
// it used to be, and not filed under the generic validate kind the startup
// gate cannot tell from a schema-compile failure.
func TestLoad_MistypedValue_IsATypedSchemaViolation(t *testing.T) {
	cfg := loadYAML(t, "schema_version: 7\nruntime: 5\n")

	got := schemaViolations(t, cfg)
	require.Len(t, got, 1, "warnings: %+v", cfg.GetWarnings())
	assert.Equal(t, config.SchemaViolation{
		File: fixtureConfigPath, KeyPath: "runtime", Expected: "string", Got: "number",
	}, *got[0])
	assert.Contains(t, got[0].Error(), fixtureConfigPath)
	assert.Contains(t, got[0].Error(), "`runtime`")
	assert.Contains(t, got[0].Error(), "expected string")
	assert.NotContains(t, got[0].Error(), "jsonschema:", "the raw validator dump must not reach the user")
}

// A nested key is reported by its full dotted path, through a dynamic map key.
func TestLoad_MistypedNestedValue_NamesTheDottedPath(t *testing.T) {
	cfg := loadYAML(t, "schema_version: 7\nagents:\n  a:\n    llm: e\n    profiles: dev\n")

	got := schemaViolations(t, cfg)
	require.Len(t, got, 1, "warnings: %+v", cfg.GetWarnings())
	assert.Equal(t, "agents.a.profiles", got[0].KeyPath)
	assert.Equal(t, "array", got[0].Expected)
	assert.Equal(t, "string", got[0].Got)
}

// A violation that is not a type mismatch (a bad enum) is still a schema
// violation, named by key path, with the constraint it broke in place of an
// expected type. It used to stay a raw-text validate warning.
func TestLoad_BadEnum_IsASchemaViolationWithItsConstraint(t *testing.T) {
	cfg := loadYAML(t, "schema_version: 7\nagents:\n  a:\n    llm: e\n    permissions:\n      sandbox: nonsense\n")

	assert.Empty(t, unknownKeyWarnings(cfg), "a bad enum is not an unknown key")
	got := schemaViolations(t, cfg)
	require.Len(t, got, 1, "warnings: %+v", cfg.GetWarnings())
	assert.Equal(t, "agents.a.permissions.sandbox", got[0].KeyPath)
	assert.Empty(t, got[0].Expected, "an enum failure has no expected TYPE")
	assert.NotEmpty(t, got[0].Constraint)
	for _, w := range cfg.GetWarnings() {
		assert.NotEqual(t, config.WarnKindValidate, w.Kind, "a file's schema breakage is no longer the generic validate kind: %+v", w)
	}
}

// An entry that matches none of an anyOf's shapes fails every alternative;
// those per-branch failures are not separate defects, so the violation is
// reported ONCE, at the entry the anyOf governs.
func TestLoad_EntryMatchingNoAlternative_IsOneViolationAtTheEntry(t *testing.T) {
	cfg := loadYAML(t, "schema_version: 7\nllm:\n  configs:\n    big:\n      type: gemini\n")

	got := schemaViolations(t, cfg)
	require.Len(t, got, 1, "warnings: %+v", cfg.GetWarnings())
	assert.Equal(t, "llm.configs.big", got[0].KeyPath)
	assert.NotEmpty(t, got[0].Constraint)
}

// An unknown key and a mistyped value in one file are two findings of two
// kinds: neither hides the other.
func TestLoad_UnknownKeyAndMistypedValue_AreBothReported(t *testing.T) {
	cfg := loadYAML(t, "schema_version: 7\nruntime: 5\nfrobnicate: 1\n")

	require.Len(t, unknownKeyWarnings(cfg), 1, "warnings: %+v", cfg.GetWarnings())
	got := schemaViolations(t, cfg)
	require.Len(t, got, 1, "warnings: %+v", cfg.GetWarnings())
	assert.Equal(t, "runtime", got[0].KeyPath)
}

// Every engine the composition root registers is a backend a config may name,
// so the schema must accept each as an llm config's type. The schema's mock
// branch once named only the bare kind, so a config bound to one of its
// registered doubles (mock-lossy, mock-launch, mock-noskills) broke the
// schema — silently, while that was a warning.
//
// A branch may require keys beside type: an opencode label is unusable
// without its model, context window and endpoint, so its entry carries the
// minimal set; any engine NOT listed is checked with type alone.
func TestLoad_EveryRegisteredEngineIsASchemaValidLLMType(t *testing.T) {
	requiredKeys := map[string]string{
		"opencode": "      model: m\n      context_window: 1\n      provider:\n        base_url: http://127.0.0.1:9/v1\n        output_limit: 1\n",
	}
	for _, name := range engines.Registry().Names(nil) {
		t.Run(string(name), func(t *testing.T) {
			cfg := loadYAML(t, "schema_version: 7\nllm:\n  configs:\n    x:\n      type: "+string(name)+"\n"+requiredKeys[string(name)])
			assert.Empty(t, schemaViolations(t, cfg), "warnings: %+v", cfg.GetWarnings())
		})
	}
}
