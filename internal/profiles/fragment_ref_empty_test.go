package profiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestProfile_EmptyFragmentEntryIsRefused pins the directory arm's parity with
// the inline one on what a fragments list may NOT contain.
//
// Every one of these decoded silently before: three to FragmentRef{Name: ""},
// which selects nothing and reports nothing, and the bare "- " to nothing at
// all -- yaml.v3 drops a null sequence item without ever calling the element's
// Unmarshaler, so the entry vanished rather than failing. A profile that
// selects no content while looking like it selects some is this codebase's
// characteristic failure, and a typo in a fragments list is the cheapest way
// to produce one.
func TestProfile_EmptyFragmentEntryIsRefused(t *testing.T) {
	for name, src := range map[string]string{
		"empty scalar":      "fragments:\n  - \"\"\n",
		"whitespace scalar": "fragments:\n  - \"   \"\n",
		"bare null item":    "fragments:\n  - \n",
		"struct empty name": "fragments:\n  - name: \"\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			var p Profile
			err := yaml.Unmarshal([]byte(src), &p)
			require.Error(t, err, "an entry naming no fragment must fail loudly, not select nothing")
			assert.Contains(t, err.Error(), "must name a fragment",
				"the error has to say what is wrong with the entry, not merely that decoding failed")
		})
	}
}

// The control: the shapes a fragments list MAY contain still decode, so the
// guard above cannot be satisfied by refusing everything.
func TestProfile_NonEmptyFragmentEntriesStillDecode(t *testing.T) {
	var p Profile
	require.NoError(t, yaml.Unmarshal([]byte("fragments:\n  - plain\n  - name: with-priority\n    priority: 5\n"), &p))
	require.Len(t, p.Fragments, 2)
	assert.Equal(t, "plain", p.Fragments[0].Name)
	assert.Equal(t, 0, p.Fragments[0].Priority)
	assert.Equal(t, "with-priority", p.Fragments[1].Name)
	assert.Equal(t, 5, p.Fragments[1].Priority)
}
