package lint

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// placeholderCorpus is the shared input set every placeholder-extracting
// implementation must agree on: bare builtins, qualified tags, value
// qualifiers, whitespace padding, adjacency, and the malformed shapes that
// decide whether a formula's references are seen at all.
var placeholderCorpus = []string{
	"",
	"{{age_days}}",
	"{{ age_days }}",
	"{{triage:impact}}",
	"{{triage:impact=capability}}",
	"{{a}}{{b}}",
	"{{a}} * (1 + {{b:c}}) / {{d:e=f}}",
	"{{}}",
	"{{ }}",
	"{{a",
	"a}}",
	"{{{a}}}",
	"{{a{b}}",
	"{{a\nb}}",
	"no placeholders at all",
	"{{ns:key=*}}",
}

// TestPlaceholderExtractionParity pins this package's placeholder extraction
// to tagschema's, which owns the syntax because CompileFormula rewrites with
// it. Any divergence means a formula tagschema compiles carries references
// lint cannot see.
func TestPlaceholderExtractionParity(t *testing.T) {
	for _, src := range placeholderCorpus {
		var mine []string
		for _, m := range formulaPlaceholderPattern.FindAllStringSubmatch(src, -1) {
			mine = append(mine, m[1])
		}
		assert.Equal(t, tagschema.Placeholders(src), mine, "src=%q", src)
	}
}
