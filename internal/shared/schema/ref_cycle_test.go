package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveSchemaRef walks `s = s.Ref` with no iteration bound, and the obvious
// reading is that a `$ref`->`$ref` cycle would spin forever inside KnownPath.
// It cannot: a *jsonschema.Schema only reaches this package through
// NewValidatorFromSchema, and the compiler REFUSES to produce one for a pure
// ref cycle -- it reports "infinite loop <location>" at compile time. So the
// unbounded loop is unreachable by construction rather than merely unlikely,
// and the property that makes it unreachable lives in the compiler, not here.
//
// These tests pin that property. If a library upgrade ever starts admitting a
// pure ref cycle, they go red and the unbounded loop in resolveSchemaRef
// becomes a live question again -- which is the whole reason to assert on the
// compile step rather than on KnownPath's return value.
func TestNewValidatorFromSchema_RefusesPureRefCycle(t *testing.T) {
	t.Run("mutual $ref cycle", func(t *testing.T) {
		v, err := NewValidatorFromSchema([]byte(`{
		  "$schema": "https://json-schema.org/draft/2020-12/schema",
		  "$id": "https://example.test/mutual-cycle.json",
		  "type": "object",
		  "properties": { "a": { "$ref": "#/$defs/x" } },
		  "$defs": {
		    "x": { "$ref": "#/$defs/y" },
		    "y": { "$ref": "#/$defs/x" }
		  }
		}`))
		require.Error(t, err, "a schema whose $ref chain never reaches a real schema must not compile")
		assert.Nil(t, v)
		assert.Contains(t, err.Error(), "infinite loop",
			"the compiler is what makes resolveSchemaRef's unbounded loop unreachable; "+
				"if this message changes, re-check that a ref cycle still cannot be compiled")
	})

	t.Run("self $ref cycle", func(t *testing.T) {
		v, err := NewValidatorFromSchema([]byte(`{
		  "$schema": "https://json-schema.org/draft/2020-12/schema",
		  "$id": "https://example.test/self-cycle.json",
		  "type": "object",
		  "properties": { "a": { "$ref": "#/$defs/x" } },
		  "$defs": { "x": { "$ref": "#/$defs/x" } }
		}`))
		require.Error(t, err)
		assert.Nil(t, v)
		assert.Contains(t, err.Error(), "infinite loop")
	})

	t.Run("a RECURSIVE schema is legal and still compiles", func(t *testing.T) {
		// The distinction that matters: recursion through an object keyword
		// (a tree of nodes) is ordinary and must keep working -- only a chain
		// of bare $refs with nothing else on it is refused. resolveSchemaRef
		// terminates here because each hop lands on a schema with no .Ref.
		v, err := NewValidatorFromSchema([]byte(`{
		  "$schema": "https://json-schema.org/draft/2020-12/schema",
		  "$id": "https://example.test/recursive.json",
		  "type": "object",
		  "properties": { "root": { "$ref": "#/$defs/node" } },
		  "$defs": {
		    "node": {
		      "type": "object",
		      "properties": { "child": { "$ref": "#/$defs/node" } },
		      "additionalProperties": false
		    }
		  }
		}`))
		require.NoError(t, err)
		assert.True(t, v.KnownPath([]string{"root", "child", "child", "child"}))
		assert.False(t, v.KnownPath([]string{"root", "child", "bogus"}))
	})
}
