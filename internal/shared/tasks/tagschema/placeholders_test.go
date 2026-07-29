package tagschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPlaceholders pins the placeholder syntax this package owns: what counts
// as a placeholder, and the exact name each one yields. Every other package
// that inspects a formula's references reads through here, so this table is
// the whole repo's definition of the mustache form.
func TestPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{"", nil},
		{"no placeholders at all", nil},
		{"{{age_days}}", []string{"age_days"}},
		{"{{ age_days }}", []string{"age_days"}},
		{"{{triage:impact}}", []string{"triage:impact"}},
		{"{{triage:impact=capability}}", []string{"triage:impact=capability"}},
		{"{{ns:key=*}}", []string{"ns:key=*"}},
		{"{{a}}{{b}}", []string{"a", "b"}},
		{"{{a}} * (1 + {{b:c}}) / {{d:e=f}}", []string{"a", "b:c", "d:e=f"}},
		{"{{a}} + {{a}}", []string{"a", "a"}},
		{"{{a\nb}}", []string{"a\nb"}},
		// Braces can never appear inside a name, so these shapes carry no
		// reference an inspector could resolve.
		{"{{}}", nil},
		{"{{a", nil},
		{"a}}", nil},
		{"{{a{b}}", nil},
	} {
		assert.Equal(t, tc.want, Placeholders(tc.src), "src=%q", tc.src)
	}
}
