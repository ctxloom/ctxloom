package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `AdditionalProperties` is a three-way union -- nil, a bool, or a
// *Schema -- and schemaChild used to type-assert only the *Schema arm. The other two
// arms fell through to "not recognized", so `additionalProperties: true`, which
// says every key name here is permitted, was walked as though it said the
// opposite.
//
// The one instance of that in this repo (config-schema.json's $defs/hook, which
// used to carry `additionalProperties: true`) is gone: the hook object was
// closed, and no schema in this repo declares `true` today. These
// cases therefore go through NewValidatorFromSchema, the public seam a second
// product's schema arrives on, which is where the union is still live.
func TestConfigValidator_KnownPath_AdditionalPropertiesUnion(t *testing.T) {
	v, err := NewValidatorFromSchema([]byte(`{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "$id": "https://example.test/addl-props.json",
	  "type": "object",
	  "properties": {
	    "open":     { "type": "object", "additionalProperties": true, "properties": { "named": {"type":"string"} } },
	    "closed":   { "type": "object", "additionalProperties": false, "properties": { "named": {"type":"string"} } },
	    "silent":   { "type": "object", "properties": { "named": {"type":"string"} } },
	    "schemaed": { "type": "object", "additionalProperties": { "type": "object", "properties": { "leaf": {"type":"string"} }, "additionalProperties": false } }
	  }
	}`))
	require.NoError(t, err)

	t.Run("additionalProperties true admits any key name", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"open", "named"}))
		assert.True(t, v.KnownPath([]string{"open", "anything_at_all"}))
	})

	t.Run("and keeps admitting them all the way down", func(t *testing.T) {
		// `true` constrains nothing about the value either, so every segment
		// below it is permitted as well. Stopping after one level would report
		// a legitimate-but-unset key as unrecognized, which is precisely the
		// distinction KnownPath exists to make.
		assert.True(t, v.KnownPath([]string{"open", "anything", "deeper", "still"}))
	})

	t.Run("additionalProperties false admits only the declared names", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"closed", "named"}))
		assert.False(t, v.KnownPath([]string{"closed", "undeclared"}))
	})

	t.Run("an ABSENT additionalProperties is not an open one", func(t *testing.T) {
		// JSON Schema's default is permissive for VALIDATION, but this walker
		// answers "does the schema name this location", and an author who
		// declared properties and said nothing else named exactly those. The
		// two in-repo schemas are authored additionalProperties:false
		// throughout, so treating silence as `true` would make every typo in
		// them resolve as known.
		assert.True(t, v.KnownPath([]string{"silent", "named"}))
		assert.False(t, v.KnownPath([]string{"silent", "undeclared"}))
	})

	t.Run("a sub-schema arm still descends into that sub-schema", func(t *testing.T) {
		assert.True(t, v.KnownPath([]string{"schemaed", "any_label", "leaf"}))
		assert.False(t, v.KnownPath([]string{"schemaed", "any_label", "bogus"}))
	})
}
