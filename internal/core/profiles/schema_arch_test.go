package profiles

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/resources"
)

// profileSchemaDoc is the embedded profile schema, decoded.
func profileSchemaDoc(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := resources.GetProfileSchema()
	require.NoError(t, err, "the profile schema must be embedded or nothing validates a profile")
	var doc struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.NotNil(t, doc.AdditionalProperties, "additionalProperties must be stated, not left to the default")
	assert.False(t, *doc.AdditionalProperties,
		"additionalProperties must stay false, or an unknown key is not an unknown key and the check is decorative")
	return doc.Properties
}

// profileYAMLFields returns Profile's serializable yaml field names.
func profileYAMLFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeFor[Profile]()
	var names []string
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// TestArch_ProfileSchema_CoversEveryProfileField binds the hand-authored schema
// to the struct it describes, in BOTH directions.
//
// It existed before against the inline profile object in config-schema.json and
// was deleted with that block; it is restored here against the schema that
// replaced it, because the defect it caught is not hypothetical. `deny_tools`
// once had yaml tags and was honoured all the way through resolution while
// appearing nowhere in the schema — and since the profile object is
// additionalProperties:false, using a real feature was reported as an unknown
// key. A field the parser honours but the schema rejects is a feature that
// cannot be used; a property the schema declares that no field reads is a key
// the docs promise and nothing consumes.
func TestArch_ProfileSchema_CoversEveryProfileField(t *testing.T) {
	props := profileSchemaDoc(t)
	require.NotEmpty(t, props, "the profile schema must declare properties for this gate to mean anything")

	fields := profileYAMLFields(t)
	require.NotEmpty(t, fields, "Profile must have serializable fields, or the reflection above is broken")

	for _, name := range fields {
		_, ok := props[name]
		assert.Truef(t, ok,
			"Profile has a serializable %q field but profile-schema.json has no property for it — "+
				"a profile using it is reported as an unknown key and the feature cannot be used. "+
				"Add it to resources/schema/input/profile-schema.json.", name)
	}

	declared := make(map[string]bool, len(fields))
	for _, f := range fields {
		declared[f] = true
	}
	for name := range props {
		assert.Truef(t, declared[name],
			"profile-schema.json declares %q but Profile has no field reading it — "+
				"the schema promises a key nothing consumes.", name)
	}
}
