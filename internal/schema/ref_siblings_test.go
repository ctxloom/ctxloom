package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U096-F06: the walker followed `$ref` by replacing the referring schema with
// its referent, which throws away every keyword declared ALONGSIDE the $ref.
// Draft 2020-12 makes $ref an in-place applicator: the siblings still apply,
// so a schema that says `{"$ref": base, "properties": {...}}` names both sets
// of keys, and the walker named only one.
//
// Measured reachability, so the row is not oversold: NO $ref in either
// in-repo schema carries a sibling object keyword today -- every one of them
// is $ref plus `description`, which is an annotation the walk does not read.
// The gap is live on NewValidatorFromSchema, the public seam a second
// product's schema arrives on, and latent on ours the day someone narrows a
// $ref'd shape in place.
func TestConfigValidator_KnownPath_RefSiblingsStillApply(t *testing.T) {
	v, err := NewValidatorFromSchema([]byte(`{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "$id": "https://example.test/ref-siblings.json",
	  "type": "object",
	  "properties": {
	    "a": {
	      "$ref": "#/$defs/base",
	      "description": "an annotation sibling, which is all our own schemas use",
	      "properties": { "extra": { "type": "string" } }
	    },
	    "chain": { "$ref": "#/$defs/middle" }
	  },
	  "$defs": {
	    "base":   { "type": "object", "properties": { "core":   { "type": "string" } } },
	    "middle": { "$ref": "#/$defs/base", "properties": { "mid": { "type": "string" } } }
	  }
	}`))
	require.NoError(t, err)

	t.Run("the referent's keys resolve", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"a", "core"}))
	})

	t.Run("the referring schema's OWN keys resolve too", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"a", "extra"}))
	})

	t.Run("a sibling declared partway along a $ref chain is not skipped", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"chain", "mid"}))
		assert.True(t, v.KnownPath([]string{"chain", "core"}))
	})

	t.Run("a key neither side declares is still unknown", func(t *testing.T) {
		assert.False(t, v.KnownPath([]string{"a", "neither"}))
		assert.False(t, v.KnownPath([]string{"chain", "neither"}))
	})

	t.Run("KnownKeys unions both sides", func(t *testing.T) {
		assert.Equal(t, []string{"core", "extra"}, v.KnownKeys([]string{"a"}))
		assert.Equal(t, []string{"core", "mid"}, v.KnownKeys([]string{"chain"}))
	})
}
