package textutil

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// legacyEllipsize reproduces the hand-rolled idiom verbatim as two of the nine
// call sites actually wrote it — internal/memory/compactor.go's
// truncateForSummary ("caps at 500 bytes", returned 503) and
// internal/compression/json.go's compressString (MaxValueLength, returned
// MaxValueLength+3). Both omitted the magic -3 pre-compensation the other
// seven sites carried, so both overflowed the cap they documented.
func legacyEllipsize(s string, maxBytes int) string {
	if len(s) > maxBytes {
		return TruncateBytes(s, maxBytes) + "..."
	}
	return s
}

// U129-F01 parity: "ellipsize to fit N bytes" is the concept every caller
// wants, and all nine hand-rolled the second half of it. The two
// implementations must agree that the RESULT fits the cap. The legacy one does
// not — that divergence is the defect.
func TestEllipsize_ParityWithLegacyIdiom(t *testing.T) {
	inputs := []string{"", "a", "short", "0123456789", "0123456789abcdefghij", "héllo wörld ünicode ✓ tail"}
	widths := []int{1, 3, 4, 5, 10, 20, 35}

	for _, in := range inputs {
		for _, w := range widths {
			got := Ellipsize(in, w)
			assert.LessOrEqual(t, len(got), w,
				"Ellipsize(%q, %d) = %q must fit the cap", in, w, got)
		}
	}

	// The divergence, asserted rather than described. Kept as the record of
	// why Ellipsize exists.
	assert.Len(t, legacyEllipsize("0123456789abcdefghij", 10), 13,
		"the legacy idiom returned cap+3 bytes whenever it truncated")
	assert.Len(t, Ellipsize("0123456789abcdefghij", 10), 10)
}

func TestEllipsize_Contract(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, ""},
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven chars", 10, "eleven ..."},
		{"0123456789abc", 10, "0123456..."},
		{"anything", 0, ""},
		{"anything", -1, ""},
		// No room for the marker: content wins over the marker.
		{"anything", 2, "an"},
		{"anything", 3, "any"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q@%d", tc.in, tc.max), func(t *testing.T) {
			assert.Equal(t, tc.want, Ellipsize(tc.in, tc.max))
		})
	}
}

// The rune-boundary guarantee TruncateBytes provides must survive: reserving
// the suffix must not push the cut into the middle of a multibyte rune.
func TestEllipsize_NeverSplitsARune(t *testing.T) {
	s := "ααααααααα" // 2 bytes per rune
	for w := 1; w <= 20; w++ {
		got := Ellipsize(s, w)
		assert.LessOrEqual(t, len(got), w)
		assert.True(t, isValidUTF8Prefix(got), "w=%d produced %q", w, got)
	}
}

func isValidUTF8Prefix(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
