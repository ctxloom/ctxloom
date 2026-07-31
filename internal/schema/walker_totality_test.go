package schema

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/resources"
)

// U096-F10: schemaChild is not total over JSON Schema's applicators, and the
// unhandled set was SILENT — a schema could grow an `if`/`then`, a
// `dependentSchemas` or a `propertyNames` and KnownPath would simply start
// answering "unknown" for everything underneath it, with nothing to say so.
// KnownPath's answer is not cosmetic: confload's override resolution uses it
// to tell a legitimate-but-unset key from an unrecognized one, and
// config/unknown_keys.go uses KnownKeys for the did-you-mean on a typo, so a
// silently-unwalkable region degrades both without a symptom anyone would
// attribute to the schema.
//
// Totality is not reachable — `not` names no keys, and a walk that answers
// "does the schema NAME this location" has no sensible child for it — so this
// pins the reachable half instead: the two schemas this repo SHIPS use only
// applicators the walker handles. Adding one it does not handle now fails
// here, naming the keyword and its location, instead of quietly costing every
// key beneath it.
var walkerHandledApplicators = map[string]bool{
	"properties":           true,
	"patternProperties":    true,
	"additionalProperties": true,
	"items":                true,
	"prefixItems":          true,
	"anyOf":                true,
	"oneOf":                true,
	"allOf":                true,
	"$ref":                 true,
}

// walkerUnhandledApplicators are the keywords that put schemas somewhere
// schemaChild does not look. Listed explicitly rather than derived by
// exclusion so that a keyword nobody has thought about yet does not silently
// count as handled.
var walkerUnhandledApplicators = []string{
	"not", "if", "then", "else",
	"dependentSchemas", "dependencies",
	"propertyNames", "unevaluatedProperties", "unevaluatedItems",
	"contains", "additionalItems",
	"$dynamicRef", "$recursiveRef",
}

func TestSchemaWalker_ShippedSchemasUseNoUnwalkableApplicator(t *testing.T) {
	// A keyword may not sit in both lists: "handled" is what schemaChild
	// follows, "unhandled" is what this test refuses, and an overlap would
	// make the refusal meaningless.
	for _, kw := range walkerUnhandledApplicators {
		assert.False(t, walkerHandledApplicators[kw], "%s is listed as both handled and unhandled", kw)
	}

	for _, name := range []string{
		"input/config-schema.json",
		"input/taskloom-config-schema.json",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := resources.GetSchema(name)
			require.NoError(t, err)
			var doc any
			require.NoError(t, json.Unmarshal(raw, &doc))

			found := unwalkableApplicators(doc, "#")
			assert.Empty(t, found,
				"%s declares an applicator the KnownPath/KnownKeys walk does not follow, so every key "+
					"beneath it now resolves as unknown: confload will warn about legitimate keys and "+
					"unknown_keys.go will lose its did-you-mean. Either teach schemaChild the keyword or "+
					"express the constraint another way.", name)
		})
	}
}

// The detector has to be able to fire, or the test above is a green that
// proves nothing.
func TestSchemaWalker_TotalityDetectorFires(t *testing.T) {
	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
	  "type": "object",
	  "properties": {
	    "a": {
	      "if":   { "properties": { "kind": { "const": "x" } } },
	      "then": { "properties": { "only_for_x": { "type": "string" } } }
	    },
	    "b": { "type": "object", "propertyNames": { "pattern": "^[a-z]+$" } }
	  }
	}`), &doc))

	found := unwalkableApplicators(doc, "#")
	sort.Strings(found)
	assert.Equal(t, []string{
		"#/properties/a: if",
		"#/properties/a: then",
		"#/properties/b: propertyNames",
	}, found)
}

// unwalkableApplicators reports every unhandled applicator keyword reachable
// from a schema node, as "<location>: <keyword>". It descends only through
// SCHEMA positions, so a user-declared property that happens to be named "if"
// or "not" is not mistaken for a keyword — `profiles.definitions` in ctxloom's
// own schema is exactly that hazard for the keyword "definitions".
func unwalkableApplicators(node any, loc string) []string {
	obj, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var found []string
	for _, kw := range walkerUnhandledApplicators {
		if _, present := obj[kw]; present {
			found = append(found, loc+": "+kw)
		}
	}

	// Keyword whose value is a map of NAME -> schema.
	for _, kw := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		sub, _ := obj[kw].(map[string]any)
		for name, child := range sub {
			found = append(found, unwalkableApplicators(child, loc+"/"+kw+"/"+name)...)
		}
	}
	// Keyword whose value is a list of schemas.
	for _, kw := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		list, _ := obj[kw].([]any)
		for i, child := range list {
			found = append(found, unwalkableApplicators(child, loc+"/"+kw+"/"+itoa(i))...)
		}
	}
	// Keyword whose value is a single schema (or, for the unions, may be a
	// bool, which carries nothing to walk).
	for _, kw := range []string{
		"items", "additionalProperties", "additionalItems", "contains",
		"not", "if", "then", "else", "propertyNames",
		"unevaluatedProperties", "unevaluatedItems",
	} {
		found = append(found, unwalkableApplicators(obj[kw], loc+"/"+kw)...)
	}
	return found
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}
