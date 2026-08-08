package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Parity pins for the collapse of joinContentText
// onto vendorreader.JoinNonEmpty. An equivalence case: the two
// agree on every input, so the parity test cannot be red and its job is to
// pin the SHARED behaviour before the collapse.
//
// The equivalence is exact by construction — both filter empty parts and join
// the rest with a blank line — and the table below drives every boundary that
// distinguishes a join implementation at all: nothing, everything empty,
// leading/interior/trailing empties, and a single element (where the
// separator must not appear).
func TestJoinContentText_MatchesVendorReaderJoinNonEmpty(t *testing.T) {
	cases := map[string][]string{
		"no blocks":            nil,
		"one block":            {"only"},
		"all empty":            {"", "", ""},
		"leading empty":        {"", "a", "b"},
		"interior empty":       {"a", "", "b"},
		"trailing empty":       {"a", "b", ""},
		"whitespace is kept":   {"  a  ", "b"},
		"embedded blank lines": {"a\n\nb", "c"},
	}
	for name, texts := range cases {
		t.Run(name, func(t *testing.T) {
			blocks := make([]contentBlock, 0, len(texts))
			for _, s := range texts {
				blocks = append(blocks, contentBlock{Type: "output_text", Text: s})
			}
			assert.Equal(t, vendorreader.JoinNonEmpty(texts), joinContentText(blocks),
				"joinContentText must agree with the shared join primitive on every input")
		})
	}
}

// The concrete shape, pinned independently of the primitive so the collapse
// is checkable without reading JoinNonEmpty: empty blocks contribute nothing
// rather than a stray blank line, and the separator is one blank line.
func TestJoinContentText_Shape(t *testing.T) {
	assert.Equal(t, "", joinContentText(nil))
	assert.Equal(t, "", joinContentText([]contentBlock{{Text: ""}, {Text: ""}}))
	assert.Equal(t, "solo", joinContentText([]contentBlock{{Text: "solo"}}))
	assert.Equal(t, "a\n\nb", joinContentText([]contentBlock{{Text: "a"}, {Text: ""}, {Text: "b"}}))
}
