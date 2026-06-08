package operations

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/resources"
)

// run --print --json is the canonical, published per-agent run shape
// (RunOneshotResult). These tests are the proof-of-pattern for vexed-cloak:
// the struct is the producer, resources/schema/run-oneshot-result-schema.json is
// the published contract, and these tests fail if the two drift apart — so a
// schema change is a deliberate, reviewable act.

const runOneshotSchemaFile = "run-oneshot-result-schema.json"

func compileEmbeddedSchema(t *testing.T, file string) *jsonschema.Schema {
	t.Helper()
	data, err := resources.GetSchema(file)
	require.NoError(t, err)
	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource(file, strings.NewReader(string(data))))
	s, err := c.Compile(file)
	require.NoError(t, err)
	return s
}

// validateJSON marshals v (a struct or map) the way the CLI would emit it and
// validates the result against the compiled schema.
func validateJSON(t *testing.T, s *jsonschema.Schema, v any) error {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(b, &generic))
	return s.Validate(generic)
}

func TestRunOneshotResult_ValidatesAgainstSchema(t *testing.T) {
	s := compileEmbeddedSchema(t, runOneshotSchemaFile)

	// Fully populated instance validates.
	require.NoError(t, validateJSON(t, s, RunOneshotResult{
		Profile:  "code-review/security",
		Output:   "all clear",
		Label:    "security",
		Backend:  "mock",
		Model:    "claude-opus-4-8",
		ExitCode: 0,
	}))

	// Minimal instance (omitempty profile/model absent) still validates.
	require.NoError(t, validateJSON(t, s, RunOneshotResult{
		Output:  "x",
		Label:   "l",
		Backend: "b",
	}))
}

func TestRunOneshotResult_SchemaRejectsUnknownField(t *testing.T) {
	// additionalProperties:false is the runtime tripwire: a field the struct
	// emits but the schema doesn't know fails validation. This proves the
	// contract rejects drift rather than silently accepting it.
	s := compileEmbeddedSchema(t, runOneshotSchemaFile)
	err := validateJSON(t, s, map[string]any{
		"output":    "x",
		"label":     "l",
		"backend":   "b",
		"exit_code": 0,
		"surprise":  "drift",
	})
	require.Error(t, err)
}

// TestRunOneshotResult_SchemaCoversEveryField is the precise struct=producer /
// schema=contract drift guard: every json-tagged field is a schema property,
// every non-omitempty field is schema-required, and no schema property lacks a
// backing field. Adding/removing/retagging a RunOneshotResult field without
// updating the schema fails here.
func TestRunOneshotResult_SchemaCoversEveryField(t *testing.T) {
	raw, err := resources.GetSchema(runOneshotSchemaFile)
	require.NoError(t, err)

	var doc struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.AdditionalProperties,
		"additionalProperties must be false so new struct fields are caught as drift")

	required := map[string]bool{}
	for _, r := range doc.Required {
		required[r] = true
	}

	rt := reflect.TypeOf(RunOneshotResult{})
	structNames := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		structNames[name] = true

		_, inSchema := doc.Properties[name]
		assert.Truef(t, inSchema, "struct field %q (json %q) is missing from the schema", rt.Field(i).Name, name)

		if !strings.Contains(opts, "omitempty") {
			assert.Truef(t, required[name], "non-omitempty field %q must be schema-required", name)
		}
	}

	for name := range doc.Properties {
		assert.Truef(t, structNames[name], "schema property %q has no backing RunOneshotResult field", name)
	}
}
