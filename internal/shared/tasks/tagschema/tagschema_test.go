package tagschema

import (
	"testing"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse_ArityScalar pins the phase-2 core case: a `tagma.arity:"<key>"=
// scalar` declaration parses into a Schema that reports IsScalar for the
// declared target and not for anything else.
func TestParse_ArityScalar(t *testing.T) {
	s, err := Parse([]string{`tagma.arity:"triage:type"=scalar`})
	require.NoError(t, err)
	assert.True(t, s.IsScalar("triage:type"))
	assert.False(t, s.IsScalar("triage:impact"))
	assert.False(t, s.IsScalar("unrelated"))
}

// TestParse_MultipleFacets proves priority_fn/decay_fn declarations are
// parsed and retrievable via Get even though phase 2 never evaluates them.
func TestParse_MultipleFacets(t *testing.T) {
	s, err := Parse([]string{
		`tagma.arity:"triage:impact"=scalar`,
		`tagma.priority_fn:"triage:impact"="{{impact}} * {{modifier_mult}} * {{age_factor}}"`,
		`tagma.decay_fn:"triage:impact"="{{impact}} * pow(0.5, {{age_days}} / {{half_life_days}})"`,
	})
	require.NoError(t, err)
	assert.True(t, s.IsScalar("triage:impact"))

	v, ok := s.Get(PriorityFnFacet, "triage:impact")
	require.True(t, ok)
	assert.Equal(t, "{{impact}} * {{modifier_mult}} * {{age_factor}}", v)

	v, ok = s.Get(DecayFnFacet, "triage:impact")
	require.True(t, ok)
	assert.Equal(t, "{{impact}} * pow(0.5, {{age_days}} / {{half_life_days}})", v)
}

// TestParse_LastDeclarationWins proves a later declaration for the same
// facet+target overwrites an earlier one, matching how a real config file's
// last line for a key would be expected to win.
func TestParse_LastDeclarationWins(t *testing.T) {
	s, err := Parse([]string{
		`tagma.arity:"triage:type"=scalar`,
		`tagma.arity:"triage:type"=multi`,
	})
	require.NoError(t, err)
	assert.False(t, s.IsScalar("triage:type"))
	v, ok := s.Get(ArityFacet, "triage:type")
	require.True(t, ok)
	assert.Equal(t, "multi", v)
}

// TestParse_RejectsMalformed pins fail-loud behavior: a declaration tagma
// itself can't parse, one with no namespace, one outside the "tagma."
// facet namespace, or one with no value must all be returned errors, never
// silently dropped.
func TestParse_RejectsMalformed(t *testing.T) {
	cases := []string{
		"not a valid tag at all !!",
		"bareKeyNoNamespace",
		`other.arity:"triage:type"=scalar`,
		`tagma:"triage:type"=scalar`, // namespace is bare "tagma", not "tagma.<facet>"
		`tagma.arity:"triage:type"`,  // no value
	}
	for _, decl := range cases {
		if _, err := Parse([]string{decl}); err == nil {
			t.Errorf("Parse(%q): expected an error", decl)
		}
	}
}

// TestGet_NilSchemaIsEmpty proves a nil *Schema degrades to "nothing
// declared" rather than panicking, mirroring schema.ConfigValidator's own
// nil-receiver-safety.
func TestGet_NilSchemaIsEmpty(t *testing.T) {
	var s *Schema
	assert.False(t, s.IsScalar("triage:type"))
	_, ok := s.Get(ArityFacet, "triage:type")
	assert.False(t, ok)
}

// TestTarget_NamespacedAndBare pins the "namespace:key" reconstruction
// Target performs, matching the literal shape a declaration's quoted key is
// written in.
func TestTarget_NamespacedAndBare(t *testing.T) {
	ns := "triage"
	assert.Equal(t, "triage:type", Target(tagma.Tag{Namespace: &ns, Key: "type"}))
	assert.Equal(t, "urgent", Target(tagma.Tag{Key: "urgent"}))
}
