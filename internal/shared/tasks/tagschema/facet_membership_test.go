package tagschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The facet constants are a closed set: every consumer reads a facet by one
// of those names and nothing reads any other. Accepting an arbitrary
// `tagma.<anything>` namespace therefore MISFILES a declaration rather than
// dropping it -- same effect as losing it, with no signal at either end. The
// taskloom config JSON Schema constrains tag_schema only to an array of
// strings, so nothing upstream catches the typo either.
func TestAdd_UnknownFacetIsARejectedDeclaration(t *testing.T) {
	cases := map[string]string{
		`tagma.arty:"triage:kind"=scalar`:     "arty",        // arity
		`tagma.priority-fn:"triage:kind"="1"`: "priority-fn", // priority_fn
		`tagma.enums:"triage:kind"="a,b"`:     "enums",
		`tagma.Arity:"triage:kind"=scalar`:    "Arity", // facets are not case-folded
	}
	for decl, facet := range cases {
		t.Run(decl, func(t *testing.T) {
			_, err := Parse([]string{decl})
			require.Error(t, err, "a declaration nothing will ever read must not parse clean")
			assert.Contains(t, err.Error(), facet, "the error must name the offending facet")
			assert.Contains(t, err.Error(), ArityFacet, "and list the facets that ARE known")
		})
	}
}

// Every declared facet constant must be accepted, and the check must be
// driven from the constants themselves rather than a second hand-written
// list -- otherwise adding a facet means remembering to edit two places.
func TestAdd_EveryDeclaredFacetIsAccepted(t *testing.T) {
	values := map[string]string{
		ArityFacet:      ArityScalar,
		PriorityFnFacet: `"1"`,
		DecayFnFacet:    `"1"`,
		EnumFacet:       `"a,b"`,
		RangeFacet:      `"0,5"`,
		HideFacet:       "true",
		TypeFacet:       SemverTypeName,
	}
	for facet, v := range values {
		decl := "tagma." + facet + `:"triage:kind"=` + v
		s, err := Parse([]string{decl})
		require.NoError(t, err, "declaration %q", decl)
		got, ok := s.Get(facet, "triage:kind")
		require.True(t, ok, "facet %q was not stored", facet)
		assert.Equal(t, strings.Trim(v, `"`), got)
	}
}
