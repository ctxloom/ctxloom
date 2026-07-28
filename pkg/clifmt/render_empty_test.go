package clifmt

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allOmitEmpty is a struct whose every field is omitted from its generic
// (json/toGeneric) form — the exact shape U154-F02 names: Render used to
// write ZERO bytes for it under text/markdown/toml, indistinguishable from a
// writer that silently failed, while json/yaml render the same value as
// `{}`/`null`.
type allOmitEmpty struct {
	Name string `json:"name,omitempty"`
	Note string `json:"note,omitempty"`
}

// TestRender_EmptyResults_NeverWriteZeroBytes pins U154-F02 across all four
// named locations (text.go, markdown.go via the shared noderender.go, and
// marshal.go's TOML path): "nothing to render" must be VISIBLE output, not
// silence indistinguishable from "the writer never ran". json is the
// negative control — it already renders these values distinctly
// (`{}`/`null`) and must keep doing so unchanged (this row does not touch
// the machine-readable json contract).
func TestRender_EmptyResults_NeverWriteZeroBytes(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		format Format
	}{
		{"all-omitempty struct, text", allOmitEmpty{}, FormatText},
		{"all-omitempty struct, markdown", allOmitEmpty{}, FormatMarkdown},
		{"all-omitempty struct, toml", allOmitEmpty{}, FormatTOML},
		{"empty scalar slice, text", []string{}, FormatText},
		{"empty scalar slice, markdown", []string{}, FormatMarkdown},
		{"nil, toml", nil, FormatTOML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, Render(&buf, tc.value, tc.format))
			assert.NotEmpty(t, buf.Bytes(),
				"Render must never write zero bytes for an empty-but-valid result — "+
					"that is indistinguishable from a writer that silently failed")
		})
	}
}

// TestRender_EmptyResults_JSONStillDistinguishesShapes is the negative
// control: json's existing `{}` (empty struct) vs `null` (nil) distinction
// is the published contract this row does NOT change (PAUSE-tier: renaming
// or removing a json field, or changing its type, would be — adding visible
// text/markdown/toml output is not).
func TestRender_EmptyResults_JSONStillDistinguishesShapes(t *testing.T) {
	var structBuf, sliceBuf, nilBuf bytes.Buffer
	require.NoError(t, Render(&structBuf, allOmitEmpty{}, FormatJSON))
	require.NoError(t, Render(&sliceBuf, []string{}, FormatJSON))
	require.NoError(t, Render(&nilBuf, nil, FormatJSON))

	assert.JSONEq(t, "{}", structBuf.String())
	assert.JSONEq(t, "[]", sliceBuf.String())
	assert.Equal(t, "null\n", nilBuf.String())
}

// TestRenderTOML_MixedMapWithNilKeyStillRenders is the companion negative
// control for renderTOML's fix: a root map that has SOME real content
// alongside a nil-valued key (go-toml/v2 silently drops just that one key)
// must still render the real content — only a WHOLLY empty root gets the
// "(none)" marker.
func TestRenderTOML_MixedMapWithNilKeyStillRenders(t *testing.T) {
	type mixed struct {
		A    int    `json:"a"`
		Gone string `json:"gone,omitempty"`
	}
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, mixed{A: 1}, FormatTOML))
	assert.Contains(t, buf.String(), "a = 1")
	assert.NotContains(t, buf.String(), "(none)")
}
