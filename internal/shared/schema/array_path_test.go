package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaChild used to handle no array keyword, so the walk stopped dead at
// the first array and KnownPath/KnownKeys answered "unknown" for everything
// beneath one — and an unknown key inside an array element, whose dotted path
// carries the array INDEX as a segment, got no did-you-mean.
func TestConfigValidator_KnownPath_DescendsIntoArrays(t *testing.T) {
	v, err := NewValidatorFromSchema(rungSchema)
	require.NoError(t, err)

	t.Run("an index segment descends into the item schema", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"agents", "coder", "rungs", "0", "action"}))
		assert.True(t, v.KnownPath([]string{"agents", "coder", "rungs", "12", "role"}))
	})

	t.Run("an unknown key under an array element is still unknown", func(t *testing.T) {
		assert.False(t, v.KnownPath([]string{"agents", "coder", "rungs", "0", "actoin"}))
	})

	t.Run("a NON-index segment does not descend into an array", func(t *testing.T) {
		// An array is indexed by position. A word here is not a key the schema
		// recognizes, and answering "known" would make every misspelling under
		// every array resolve.
		assert.False(t, v.KnownPath([]string{"agents", "coder", "rungs", "action"}))
		assert.False(t, v.KnownPath([]string{"agents", "coder", "rungs", "-1"}))
	})

	t.Run("KnownKeys enumerates an array element's declared names", func(t *testing.T) {
		keys := v.KnownKeys([]string{"agents", "coder", "rungs", "0"})
		assert.Contains(t, keys, "action", "the did-you-mean for a mistyped field comes from here")
		assert.Contains(t, keys, "role")
		assert.Contains(t, keys, "timeout")
	})
}

// rungSchema is a map of named entries, each holding an array of objects
// with a required member — the shape the array walk and ValidateAt must
// handle, whatever product schema carries it.
var rungSchema = []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://example.test/rungs.json",
  "type": "object",
  "properties": {
    "agents": {
      "type": "object",
      "additionalProperties": {
        "type": "object",
        "properties": {
          "rungs": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["action"],
              "additionalProperties": false,
              "properties": {
                "action": {"type": "string"},
                "role": {"type": "string"},
                "timeout": {"type": "string"}
              }
            }
          }
        },
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false
}`)

// prefixItems is the other draft 2020-12 array applicator; ctxloom's own
// schema does not use it today, but NewValidatorFromSchema is a public seam a
// second product's schema goes through, so the walker handles both forms.
func TestConfigValidator_KnownPath_PrefixItems(t *testing.T) {
	v, err := NewValidatorFromSchema([]byte(`{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "$id": "https://example.test/prefix-items.json",
	  "type": "object",
	  "properties": {
	    "pair": {
	      "type": "array",
	      "prefixItems": [
	        { "type": "object", "properties": { "first": {"type":"string"} }, "additionalProperties": false },
	        { "type": "object", "properties": { "second": {"type":"string"} }, "additionalProperties": false }
	      ],
	      "items": { "type": "object", "properties": { "rest": {"type":"string"} }, "additionalProperties": false }
	    }
	  }
	}`))
	require.NoError(t, err)

	assert.True(t, v.KnownPath([]string{"pair", "0", "first"}))
	assert.True(t, v.KnownPath([]string{"pair", "1", "second"}))
	assert.False(t, v.KnownPath([]string{"pair", "0", "second"}), "positional schemas are not interchangeable")

	// Past the end of prefixItems, `items` governs the remaining positions.
	assert.True(t, v.KnownPath([]string{"pair", "2", "rest"}))
}
