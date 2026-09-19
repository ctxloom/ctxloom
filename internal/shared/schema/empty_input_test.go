package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ValidateBytes used to never check for empty input. Handed a file with
// nothing in it, it parsed nil, validated nil, and returned nil — "validated,
// no problems found", having examined no document at all.
//
// The two schemas this repo ships happen to hide that, because each declares
// a root `"type": "object"` and null is not an object. But that is a property
// of those two documents, not of this API, and NewValidatorFromSchema is a
// public seam whose caller authors its own schema. A root without a type
// constraint — or with one that admits null — put the caller straight back on
// exit-0-having-checked-nothing.
func TestValidateBytes_RefusesAnEmptyDocument(t *testing.T) {
	permissive, err := NewValidatorFromSchema([]byte(`{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "$id": "https://example.test/permissive.json"
	}`))
	require.NoError(t, err)

	t.Run("no bytes at all", func(t *testing.T) {
		err := permissive.ValidateBytes(nil)
		require.Error(t, err, "a schema that constrains nothing must not turn an empty file into a pass")
		assert.Contains(t, err.Error(), "no YAML document")
	})

	t.Run("whitespace only", func(t *testing.T) {
		err := permissive.ValidateBytes([]byte("\n\n   \n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no YAML document")
	})

	t.Run("comments only", func(t *testing.T) {
		// The same fact as an empty file: the parser produces no document.
		err := permissive.ValidateBytes([]byte("# nothing but a note\n# and another\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no YAML document")
	})

	t.Run("an explicit null IS a document and is validated, not refused", func(t *testing.T) {
		// The guard must not become "reject anything that decodes to nil".
		// `null` is a value a schema may legitimately permit, and refusing it
		// here would be this package inventing a constraint the schema does
		// not state.
		assert.NoError(t, permissive.ValidateBytes([]byte("null")))
		assert.NoError(t, permissive.ValidateBytes([]byte("~")))

		nullable, err := NewValidatorFromSchema([]byte(`{
		  "$schema": "https://json-schema.org/draft/2020-12/schema",
		  "$id": "https://example.test/nullable.json",
		  "type": "null"
		}`))
		require.NoError(t, err)
		assert.NoError(t, nullable.ValidateBytes([]byte("null")))
	})

	t.Run("an empty MAPPING is a document and still validates", func(t *testing.T) {
		assert.NoError(t, permissive.ValidateBytes([]byte("{}")))
	})
}

// The embedded ctxloom schema reached the right answer for the wrong reason.
// It should keep reaching it, and now say why.
func TestValidateBytes_EmptyConfigNamesTheEmptiness(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	err = v.ValidateBytes(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no YAML document",
		"the diagnostic should say the file is empty, not that null is not an object")
}
