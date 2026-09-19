package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/resources"
)

// The type and its error strings are named for ctxloom
// config while the type is product-agnostic. The naming half does not hold —
// `ConfigValidator`, "failed to add config schema resource" and "failed to
// compile config schema" all say *config*, and the only mention of ctxloom in
// the package is on NewConfigValidator, which genuinely IS the ctxloom
// convenience wrapper and is documented as the one ctxloom-specific thing here.
//
// What the row is right about is that the type serves two products, and THAT
// is what was untested: nothing pinned that a validator built from another
// product's schema stays independent of ctxloom's. A refuted row whose live
// use ships unguarded is worse than the row. So this pins the independence
// directly, in both directions.
func TestValidator_IsIndependentOfCtxloomsOwnSchema(t *testing.T) {
	taskloomSchema, err := resources.GetSchema("input/taskloom-config-schema.json")
	require.NoError(t, err)
	tl, err := NewValidatorFromSchema(taskloomSchema)
	require.NoError(t, err)

	cl, err := NewConfigValidator()
	require.NoError(t, err)

	t.Run("each validator knows only its own product's keys", func(t *testing.T) {
		assert.True(t, tl.KnownPath([]string{"homing"}), "a taskloom key must resolve in taskloom's validator")
		assert.False(t, cl.KnownPath([]string{"homing"}), "and must NOT resolve in ctxloom's")

		assert.True(t, cl.KnownPath([]string{"default_agent"}), "a ctxloom key must resolve in ctxloom's validator")
		assert.False(t, tl.KnownPath([]string{"default_agent"}), "and must NOT resolve in taskloom's")
	})

	t.Run("validation follows the schema it was given", func(t *testing.T) {
		assert.NoError(t, tl.ValidateBytes([]byte("homing: repo\n")))
		assert.Error(t, tl.ValidateBytes([]byte("default_agent: dev\n")),
			"taskloom's schema is additionalProperties:false; a ctxloom key is an unknown key there")
	})

	t.Run("no diagnostic from the shared machinery names one product", func(t *testing.T) {
		// The generic constructor must not stamp "ctxloom" onto a second
		// product's failure. NewConfigValidator's own doc is the one place
		// ctxloom is named, and it is named there on purpose.
		_, err := NewValidatorFromSchema([]byte(`{"type":`))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "ctxloom")
	})
}
