package bundles

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
