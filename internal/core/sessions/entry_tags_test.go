package sessions

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tagName strips a struct tag's options ("harp_name,omitempty" -> "harp_name").
func tagName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return name
}

// TestEntry_JSONTagsMirrorYAMLKeys pins the invariant Entry's doc comment
// asserts: the YAML keys are the sidecar's on-disk contract, and every field's
// json tag names the SAME snake_case key.
//
// The claim is not self-enforcing -- a field can acquire a camelCase json tag,
// or a json key with no yaml counterpart, with nothing failing -- and the cost
// of it drifting is a persisted-format contract that reads one way in the file
// and another on a wire. The single documented asymmetry is asserted by name so
// adding a second one is a decision, not an accident.
func TestEntry_JSONTagsMirrorYAMLKeys(t *testing.T) {
	rt := reflect.TypeOf(Entry{})
	require.Positive(t, rt.NumField(), "Entry must have fields for this to assert anything")

	// computedOnRead lists the fields deliberately absent from the sidecar
	// because they are derived on read: the harp name from the directory,
	// summary and detail from essence.md, the rest from stat. Everything
	// else must mirror.
	computedOnRead := map[string]bool{
		"HarpName":                true,
		"Summary":                 true,
		"Detail":                  true,
		"LastActivity":            true,
		"CanonicalTranscriptPath": true,
	}

	seenComputed := map[string]bool{}
	mirrored := 0
	for i := range rt.NumField() {
		f := rt.Field(i)
		y := tagName(f.Tag.Get("yaml"))
		j := tagName(f.Tag.Get("json"))

		require.NotEmpty(t, y, "field %s must carry a yaml tag: the yaml keys ARE the on-disk contract", f.Name)

		if y == "-" {
			assert.True(t, computedOnRead[f.Name],
				"field %s is excluded from the sidecar (yaml:\"-\") but is not a documented computed-on-read field; adding a persisted-format exemption is a decision, not a tag edit", f.Name)
			seenComputed[f.Name] = true
			continue
		}

		assert.False(t, computedOnRead[f.Name],
			"field %s is documented as computed-on-read but now persists to yaml key %q", f.Name, y)
		require.Equal(t, y, j,
			"field %s: the json tag must mirror the yaml key exactly (yaml %q vs json %q)", f.Name, y, j)
		assert.Equal(t, strings.ToLower(y), y, "yaml key %q must be snake_case, never camelCase", y)
		mirrored++
	}

	assert.Equal(t, computedOnRead, seenComputed,
		"every documented computed-on-read field must still be marked yaml:\"-\"")
	require.Positive(t, mirrored, "the walk must have reached persisted fields; zero proves nothing")
}

// TestEntry_DerivedFieldsAreTheOnlyJSONOnlyKeys pins the asymmetry by name:
// the derived fields are visible over JSON but absent from the sidecar. A new
// one appearing silently would mean a projection could show a key the
// sidecar cannot round-trip.
func TestEntry_DerivedFieldsAreTheOnlyJSONOnlyKeys(t *testing.T) {
	rt := reflect.TypeOf(Entry{})
	var jsonOnly []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		y := tagName(f.Tag.Get("yaml"))
		j := tagName(f.Tag.Get("json"))
		if y == "-" && j != "-" && j != "" {
			jsonOnly = append(jsonOnly, f.Name+"="+j)
		}
	}
	assert.Equal(t, []string{
		"HarpName=harp_name",
		"Summary=summary",
		"Detail=detail",
		"CanonicalTranscriptPath=canonical_transcript_path",
	}, jsonOnly)
}
