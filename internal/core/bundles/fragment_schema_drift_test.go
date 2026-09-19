package bundles

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schema"
	"github.com/ctxloom/ctxloom/resources"
)

// fragment-schema.json is the hand-authored contract for fragment YAML. There is
// no single Go struct that fully owns the standalone-fragment file shape, but
// BundleFragment is the concrete struct whose fields appear in fragment content,
// so every serializable BundleFragment field must be permitted by the schema —
// with additionalProperties:false, a field the schema omits would make an
// otherwise-valid fragment fail validation. This is the input-schema drift gate
// for fragments (the authored-input counterpart to the generated output checks).
func TestFragmentSchema_PermitsEveryBundleFragmentField(t *testing.T) {
	raw, err := resources.GetSchema("input/fragment-schema.json")
	require.NoError(t, err)

	var doc struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.AdditionalProperties,
		"fragment-schema additionalProperties must be false so unknown keys are rejected")

	rt := reflect.TypeFor[BundleFragment]()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		_, ok := doc.Properties[name]
		assert.Truef(t, ok,
			"BundleFragment has a serializable %q field that fragment-schema does not permit; "+
				"add it to resources/schema/input/fragment-schema.json.", name)
	}
}

// TestFragmentSchema_ExportsBlockIsOpaque: the per-engine export block is
// OPAQUE — keyed by whatever engine name the registry knows, each block an
// object the named engine decodes against its own ExportSchema. The schema
// must therefore permit ANY engine key (the registry, not the schema, names
// engines) and constrain each value only to an object. Both spellings —
// `exports:` and the retired `llm:` an older bundle still carries — read.
func TestFragmentSchema_ExportsBlockIsOpaque(t *testing.T) {
	raw, err := resources.GetSchema("input/fragment-schema.json")
	require.NoError(t, err)

	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	for _, key := range []string{"exports", "llm"} {
		rawBlock, ok := doc.Properties[key]
		require.True(t, ok, "fragment-schema must model the %q block", key)
		var block struct {
			Type                 string          `json:"type"`
			Properties           json.RawMessage `json:"properties"`
			AdditionalProperties json.RawMessage `json:"additionalProperties"`
		}
		require.NoError(t, json.Unmarshal(rawBlock, &block))
		assert.Equal(t, "object", block.Type)
		assert.Empty(t, block.Properties, "%q must name no engine: the registry does", key)
		assert.JSONEq(t, `{"type":"object"}`, string(block.AdditionalProperties),
			"%q permits any engine key and constrains each block only to an object", key)
	}
}

// TestFragmentSchema_ValidatesAnyEngineBlock is the payload-level
// counterpart: a document carrying blocks for a registered engine and for
// one nobody registers validates, under either key; a scalar where a block
// belongs does not.
func TestFragmentSchema_ValidatesAnyEngineBlock(t *testing.T) {
	raw, err := resources.GetSchema("input/fragment-schema.json")
	require.NoError(t, err)

	v, err := schema.NewValidatorFromSchema(raw)
	require.NoError(t, err)

	for _, key := range []string{"exports", "llm"} {
		doc := []byte(`
content: "some content"
` + key + `:
  claude-code:
    enabled: true
    description: "Review code"
  some-future-engine:
    anything: [1, 2]
`)
		assert.NoError(t, v.ValidateBytes(doc), "%s: any engine's block must validate", key)
		assert.Error(t, v.ValidateBytes([]byte("content: x\n"+key+":\n  claude-code: yes\n")), "%s: a scalar is not a block", key)
	}
}
