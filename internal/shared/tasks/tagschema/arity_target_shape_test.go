package tagschema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An arity=scalar declaration only constrains anything when its target has
// the "namespace:key" shape a VALUED task tag reconstructs to (see Target):
// the write seam's scalar collapse ignores every tag without a namespace, and
// a target carrying a value or no namespace equals no tag's Target at all. A
// declaration against any other shape would parse clean and enforce nothing,
// so add refuses it.
func TestParse_RefusesArityTargetThatCannotBindAValuedTag(t *testing.T) {
	inert := []string{
		`tagma.arity:area=scalar`,                 // a bare namespace: area:cli has no value to collapse
		`tagma.arity:repo=scalar`,                 // the measured case: repo:alpha and repo:beta both survived
		`tagma.arity:"triage:kind=defect"=scalar`, // a value inside the target equals no Target
		`tagma.arity:"!!"=scalar`,                 // not a tag at all
	}
	for _, decl := range inert {
		t.Run(decl, func(t *testing.T) {
			_, err := Parse([]string{decl})
			require.ErrorIs(t, err, errArityTargetShape)
		})
	}
}

func TestParse_AcceptsNamespacedKeyArityTarget(t *testing.T) {
	s, err := Parse([]string{`tagma.arity:"triage:kind"=scalar`})
	require.NoError(t, err)
	require.True(t, s.IsScalar("triage:kind"))
}
