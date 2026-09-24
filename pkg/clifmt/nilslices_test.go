package clifmt

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nilSliceItem struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type nilSlicePayload struct {
	Local   []nilSliceItem       `json:"local"`
	Remote  []*nilSliceItem      `json:"remote"`
	ByKey   map[string][]string  `json:"by_key"`
	Any     any                  `json:"any"`
	Skipped []string             `json:"skipped,omitempty"`
	Raw     []byte               `json:"raw"`
	Owned   ownsItsEncoding      `json:"owned"`
	Nested  *nilSlicePayloadLeaf `json:"nested"`
	hidden  []string
}

type nilSlicePayloadLeaf struct {
	Items []int `json:"items"`
}

// ownsItsEncoding has its own MarshalJSON that reports whether its slice was
// nil — the normalizer must not reach inside a type that owns its encoding.
type ownsItsEncoding struct{ Inner []string }

func (o ownsItsEncoding) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]bool{"inner_was_nil": o.Inner == nil})
}

func renderDecoded(t *testing.T, v any, f Format) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, v, f))
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), buf.String())
	return got
}

// TestRenderJSON_NilSlicesRenderAsEmptyLists asserts the KEY a jq caller
// iterates, not just that the output parses: `null` parses fine and is the
// wrong answer (`.local[]` dies with "Cannot iterate over null").
func TestRenderJSON_NilSlicesRenderAsEmptyLists(t *testing.T) {
	in := nilSlicePayload{
		Remote: []*nilSliceItem{{Name: "r"}},
		ByKey:  map[string][]string{"k": nil},
		Any:    []string(nil),
		Nested: &nilSlicePayloadLeaf{},
	}
	got := renderDecoded(t, in, FormatJSON)

	assert.Equal(t, []any{}, got["local"], "top-level nil slice field")
	remote := got["remote"].([]any)
	require.Len(t, remote, 1)
	assert.Equal(t, []any{}, remote[0].(map[string]any)["tags"], "nil slice inside a pointer element")
	assert.Equal(t, []any{}, got["by_key"].(map[string]any)["k"], "nil slice as a map value")
	assert.Equal(t, []any{}, got["any"], "typed nil slice behind an interface")
	assert.Equal(t, []any{}, got["nested"].(map[string]any)["items"], "nil slice behind a struct pointer")

	// Deliberately untouched.
	assert.NotContains(t, got, "skipped", "omitempty still omits an empty list")
	assert.Nil(t, got["raw"], "[]byte is a base64 string, not a list: nil stays null")
	assert.Equal(t, map[string]any{"inner_was_nil": true}, got["owned"], "a json.Marshaler owns its encoding")

	// The input is copied, never mutated.
	assert.Nil(t, in.Local)
	assert.Nil(t, in.Remote[0].Tags)
	assert.Nil(t, in.ByKey["k"])
	assert.Nil(t, in.Nested.Items)
}

func TestRenderJSON_TopLevelNilSliceIsEmptyList(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, []nilSliceItem(nil), FormatJSON))
	assert.Equal(t, "[]\n", buf.String())
}

func TestRenderJSON_NilMapStaysNull(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, struct {
		M map[string]int `json:"m"`
	}{}, FormatJSON))
	assert.JSONEq(t, `{"m":null}`, buf.String())
}

// TestRenderYAML_NilSlicesAgreeWithJSON pins that yaml (which shares
// toGeneric with toml) gets the same `[]` as json rather than `null`.
func TestRenderYAML_NilSlicesAgreeWithJSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, nilSliceItem{Name: "a"}, FormatYAML))
	assert.Contains(t, buf.String(), "tags: []")
}

type nilSliceCycle struct {
	Next  *nilSliceCycle `json:"next"`
	Items []string       `json:"items"`
}

// TestRenderJSON_PointerCycleStillErrors: the normalizer must terminate on a
// cycle and leave encoding/json to report it, as it always has.
func TestRenderJSON_PointerCycleStillErrors(t *testing.T) {
	n := &nilSliceCycle{}
	n.Next = n
	var buf bytes.Buffer
	assert.Error(t, Render(&buf, n, FormatJSON))
}
