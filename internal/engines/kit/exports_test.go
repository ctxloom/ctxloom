package kit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testBlock struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Model   string `json:"model,omitempty"`
}

var testBlockSchema = []byte(`{"type":"object","properties":{"enabled":{"type":"boolean"},"model":{"type":"string"}},"additionalProperties":false}`)

// The absent block is the zero block; a valid one decodes; one the schema
// refuses is an error naming the engine and decodes to nothing.
func TestExportDecoder(t *testing.T) {
	decode := ExportDecoder[testBlock]("eng-x", testBlockSchema)

	b, err := decode(nil)
	require.NoError(t, err)
	assert.Equal(t, testBlock{}, b)

	b, err = decode([]byte(`{"enabled":false,"model":"m"}`))
	require.NoError(t, err)
	require.NotNil(t, b.Enabled)
	assert.False(t, *b.Enabled)
	assert.Equal(t, "m", b.Model)

	for _, raw := range []string{`{"enabled":"yes"}`, `{"unknown":1}`} {
		b, err = decode([]byte(raw))
		require.Error(t, err, raw)
		assert.Contains(t, err.Error(), "eng-x: export block")
		assert.Equal(t, testBlock{}, b)
	}
}

// A schema that does not compile refuses every present block, naming the
// engine; the absent block still decodes.
func TestExportDecoder_BrokenSchema(t *testing.T) {
	decode := ExportDecoder[testBlock]("eng-x", []byte(`{not json`))
	_, err := decode([]byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "eng-x: export schema")
	_, err = decode(nil)
	assert.NoError(t, err)
}
