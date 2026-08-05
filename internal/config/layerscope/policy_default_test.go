package layerscope

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/resources"
)

// TestDefaultPolicy_ExhaustiveAgainstSchema is the exhaustiveness gate the
// design doc promises: "DefaultPolicy is EXHAUSTIVE against the config schema
// by test — a key the schema knows and the policy does not is a failure, so
// no key can be added without its scope being decided." It walks
// resources/schema/input/config-schema.json directly (the raw JSON document,
// not the compiled validator: schema.ConfigValidator's KnownKeys collapses a
// dynamic map's own "additionalProperties" schema into an empty property set,
// which would make a wildcard level like `agents` indistinguishable from a
// genuine leaf) and asserts every LEAF path it finds resolves via
// Policy.Lookup. A leaf is anywhere maps.Flatten would stop: a scalar/array
// value, or an object with neither declared properties nor a dynamic
// additionalProperties schema to recurse into.
func TestDefaultPolicy_ExhaustiveAgainstSchema(t *testing.T) {
	schemaData, err := resources.GetConfigSchema()
	if err != nil {
		t.Fatalf("load config schema: %v", err)
	}
	root := mustParseSchema(t, schemaData)
	policy := DefaultPolicy()

	var missing []string
	walkSchemaLeaves(root, root, nil, func(path []string) {
		joined := strings.Join(path, ".")
		if _, ok := policy.Lookup(path); !ok {
			missing = append(missing, joined)
		}
	})

	if len(missing) > 0 {
		t.Errorf("DefaultPolicy has no rule covering these schema leaves (add a Rule and decide their Scope):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// mustParseSchema decodes the config schema's raw JSON into a generic tree.
func mustParseSchema(t *testing.T, data []byte) map[string]any {
	t.Helper()
	m, err := decodeJSONObject(data)
	if err != nil {
		t.Fatalf("parse config schema JSON: %v", err)
	}
	return m
}
