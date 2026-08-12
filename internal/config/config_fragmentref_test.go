package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestFragmentRef_EmptyEntry_Rejected pins U049-F19: a null/empty entry in a
// profile's fragments list decoded to FragmentRef{Name: ""} silently and flowed
// through the resolver to resolve to nothing. An empty fragment entry is a typo,
// never an intent, and must fail at decode rather than vanish. The bare "- "
// case is the headline one — yaml.v3 skips a value's Unmarshaler for a null
// node, so the ref-level check alone cannot see it; the profile decode backstops
// it.
func TestFragmentRef_EmptyEntry_Rejected(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"bare_null_entry", "fragments:\n  - go-style\n  - \n  - testing\n"},
		{"explicit_empty_string", "fragments:\n  - go-style\n  - \"\"\n"},
		{"whitespace_only", "fragments:\n  - go-style\n  - \"   \"\n"},
		{"struct_empty_name", "fragments:\n  - name: \"\"\n    priority: 3\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Profile
			err := yaml.Unmarshal([]byte(tc.yaml), &p)
			require.Error(t, err, "an empty fragments entry must be rejected at decode")
			assert.Contains(t, err.Error(), "fragment", "the error must name what is empty")
		})
	}
}

// TestFragmentRef_NonEmptyEntries_StillDecode is the control: real entries must
// still decode to their name in both the scalar and struct forms.
func TestFragmentRef_NonEmptyEntries_StillDecode(t *testing.T) {
	var p Profile
	require.NoError(t, yaml.Unmarshal([]byte("fragments:\n  - go-style\n  - name: testing\n    priority: 10\n"), &p))
	require.Len(t, p.Fragments, 2)
	assert.Equal(t, "go-style", p.Fragments[0].Name)
	assert.Equal(t, "testing", p.Fragments[1].Name)
	assert.Equal(t, 10, p.Fragments[1].Priority)
}
