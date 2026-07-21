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
// parsed and retrievable via Get (formula.go's CompileFormula/
// internal/shared/tasks/priority is what actually evaluates them; this test
// only pins the generic parse/Get plumbing against arbitrary example text).
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

func TestPriorityFnAndDecayFn_RetrieveDeclaredFormulas(t *testing.T) {
	s, err := Parse([]string{
		`tagma.priority_fn:"triage:impact"="{{triage:impact}} * {{age_factor}}"`,
		`tagma.decay_fn:"triage:impact"="1 + {{age_days}} / 365"`,
	})
	require.NoError(t, err)

	v, ok := s.PriorityFn("triage:impact")
	require.True(t, ok)
	assert.Equal(t, "{{triage:impact}} * {{age_factor}}", v)

	v, ok = s.DecayFn("triage:impact")
	require.True(t, ok)
	assert.Equal(t, "1 + {{age_days}} / 365", v)

	_, ok = s.PriorityFn("unrelated")
	assert.False(t, ok)
}

// TestTargets_ReturnsSortedDeclaredTargetsForFacet pins Targets' contract:
// every target declared for a facet, sorted, and only that facet — a target
// declared under a different facet must not leak in.
func TestTargets_ReturnsSortedDeclaredTargetsForFacet(t *testing.T) {
	s, err := Parse([]string{
		`tagma.priority_fn:"triage:severity"="{{triage:severity}}"`,
		`tagma.priority_fn:"triage:aardvark"="{{triage:aardvark}}"`,
		`tagma.decay_fn:"triage:severity"="1"`,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"triage:aardvark", "triage:severity"}, s.Targets(PriorityFnFacet))
	assert.Equal(t, []string{"triage:severity"}, s.Targets(DecayFnFacet))
	assert.Empty(t, s.Targets(EnumFacet), "an undeclared facet has no targets")
}

// TestTargets_NilSchemaIsEmpty mirrors every other accessor's nil-receiver
// safety.
func TestTargets_NilSchemaIsEmpty(t *testing.T) {
	var s *Schema
	assert.Empty(t, s.Targets(PriorityFnFacet))
}

func TestEnum_ParsesCommaSeparatedListAndDropsEmptyEntries(t *testing.T) {
	s, err := Parse([]string{
		`tagma.enum:"triage:type"="correctness,security, docs ,,build"`,
	})
	require.NoError(t, err)

	v, ok := s.Enum("triage:type")
	require.True(t, ok)
	assert.Equal(t, []string{"correctness", "security", "docs", "build"}, v)

	_, ok = s.Enum("unrelated")
	assert.False(t, ok)
}

func TestRange_ParsesMinMax(t *testing.T) {
	s, err := Parse([]string{`tagma.range:"triage:impact"="0,5"`})
	require.NoError(t, err)

	min, max, ok, err := s.Range("triage:impact")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0.0, min)
	assert.Equal(t, 5.0, max)

	_, _, ok, err = s.Range("unrelated")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRange_MalformedValueIsAReturnedError(t *testing.T) {
	s, err := Parse([]string{`tagma.range:"triage:impact"="not-a-range"`})
	require.NoError(t, err)

	_, _, _, err = s.Range("triage:impact")
	require.Error(t, err)
}

func TestEnumRangePriorityFnDecayFn_NilSchemaIsEmpty(t *testing.T) {
	var s *Schema
	_, ok := s.Enum("triage:type")
	assert.False(t, ok)
	_, _, ok, err := s.Range("triage:impact")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok = s.PriorityFn("triage:impact")
	assert.False(t, ok)
	_, ok = s.DecayFn("triage:impact")
	assert.False(t, ok)
}

// TestHideFacts_NilSchemaIsEmpty mirrors the nil-receiver-safety every other
// accessor on Schema has: a nil Schema (no tag_schema resolved at all) has
// no hide declarations, same as an explicitly empty one.
func TestHideFacts_NilSchemaIsEmpty(t *testing.T) {
	var s *Schema
	assert.Empty(t, s.HideFacts())
}

// TestHideFacts_EmptySchemaIsEmpty pins that a Schema with no tagma.hide:*
// declarations at all yields no facts — the caller (cmd/taskloom's
// hideConfigFor) then hands tagma.HideConfigFromPatterns an empty slice,
// which still seeds tagma's own implicit tagma:*=true default.
func TestHideFacts_EmptySchemaIsEmpty(t *testing.T) {
	s, err := Parse([]string{`tagma.arity:"triage:type"=scalar`})
	require.NoError(t, err)
	assert.Empty(t, s.HideFacts())
}

// TestHideFacts_DecodesDeclaredTargets pins the decode shape HideFacts
// promises: one tagma.HideFact per tagma.hide:<target>=<bool> declaration,
// target carried through verbatim (the quoted key, opaque to this package),
// hide decoded from "true"/"false".
func TestHideFacts_DecodesDeclaredTargets(t *testing.T) {
	s, err := Parse([]string{
		`tagma.hide:"triage:cwe"=true`,
		`tagma.hide:"*:secret"=false`,
	})
	require.NoError(t, err)

	facts := s.HideFacts()
	require.Len(t, facts, 2)
	byTarget := map[string]bool{}
	for _, f := range facts {
		byTarget[f.Target] = f.Hide
	}
	assert.Equal(t, map[string]bool{"triage:cwe": true, "*:secret": false}, byTarget)
}

// TestHideFacts_UninterpretableValueIsSkipped mirrors tagma's own hideFact
// decoding: a declared value that isn't exactly "true"/"false" configures
// nothing rather than erroring or defaulting either way.
func TestHideFacts_UninterpretableValueIsSkipped(t *testing.T) {
	s, err := Parse([]string{`tagma.hide:"triage:cwe"="yes"`})
	require.NoError(t, err)
	assert.Empty(t, s.HideFacts())
}

// TestHideFacts_BuildsSameEffectiveHideSetAsTagma proves the reconciliation
// this feature depends on: tagma.HideConfigFromPatterns(schema.HideFacts())
// hides exactly the declared target PLUS tagma's own implicit tagma:*
// default, matching what a caller that read tagma.hide tags straight off a
// tag collection (tagma.HideConfigFromTags) would produce.
func TestHideFacts_BuildsSameEffectiveHideSetAsTagma(t *testing.T) {
	s, err := Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)

	cfg := tagma.HideConfigFromPatterns(s.HideFacts())

	cwe, err := tagma.ParseTag("triage:cwe=79")
	require.NoError(t, err)
	assert.True(t, tagma.TagHidden(cwe, cfg), "the declared target must be hidden")

	other, err := tagma.ParseTag("triage:type=security")
	require.NoError(t, err)
	assert.False(t, tagma.TagHidden(other, cfg), "an undeclared target must stay visible")

	meta, err := tagma.ParseTag("tagma.arity:whatever=scalar")
	require.NoError(t, err)
	assert.True(t, tagma.TagHidden(meta, cfg), "tagma's own implicit tagma:* default must still hide the meta-family")
}
