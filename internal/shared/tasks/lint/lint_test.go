package lint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

func mustSchema(t *testing.T, decls ...string) *tagschema.Schema {
	t.Helper()
	s, err := tagschema.Parse(decls)
	require.NoError(t, err)
	return s
}

// tripleSchema mirrors internal/taskloom/config.DefaultTagSchema's
// arity/enum/range declarations for the triage standard.
func tripleSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	return mustSchema(t,
		`tagma.arity:"triage:type"=scalar`,
		`tagma.arity:"triage:impact"=scalar`,
		`tagma.enum:"triage:type"="correctness,security,performance,reliability,docs,build,feature,chore"`,
		`tagma.range:"triage:impact"="0,5"`,
	)
}

func TestLint_CleanDataProducesNoViolations(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "a", Tags: []string{"triage:type=security", "triage:impact=3"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

func TestLint_FlagsTypeNotInEnum(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "bad-type", Tags: []string{"triage:type=sparkles"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, "bad-type", result.Violations[0].HarpID)
	assert.Contains(t, result.Violations[0].Reason, "triage:type")
	assert.Contains(t, result.Violations[0].Reason, "sparkles")
}

func TestLint_FlagsImpactOutOfRange(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "too-high", Tags: []string{"triage:impact=9"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, "too-high", result.Violations[0].HarpID)
	assert.Contains(t, result.Violations[0].Reason, "outside the declared range")
}

func TestLint_FlagsImpactThatDoesNotParseAsANumber(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "non-numeric", Tags: []string{`triage:impact="not-a-number"`}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 1)
	assert.Contains(t, result.Violations[0].Reason, "does not parse as a number")
}

// TestLint_FlagsScalarCardinalityViolation covers the "foreign/legacy data"
// case the write-seam collapse should prevent going forward: two distinct
// values for the same arity=scalar target on one task.
func TestLint_FlagsScalarCardinalityViolation(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "double-typed", Tags: []string{"triage:type=security", "triage:type=docs"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	// Both a distinct-value-count violation AND (if either value isn't in
	// the enum) an enum violation could fire; here both values ARE valid
	// enum members, so only the cardinality violation should appear.
	require.Len(t, result.Violations, 1)
	assert.Contains(t, result.Violations[0].Reason, "arity=scalar")
	assert.Contains(t, result.Violations[0].Reason, "2 distinct values")
}

func TestLint_NoEnumOrRangeDeclaredSkipsThoseChecks(t *testing.T) {
	// A bare arity-only schema (no enum/range facets at all): an
	// out-of-vocabulary type or an out-of-range impact should raise no
	// violation for those checks specifically — only what's actually
	// declared is enforced.
	schema := mustSchema(t, `tagma.arity:"triage:type"=scalar`)
	all := []tasks.Task{
		{HarpID: "a", Tags: []string{"triage:type=anything-goes", "triage:impact=999"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

// TestLint_NilSchemaSkipsEverything proves EVERY check (enum, range —
// including the "must parse as a number" check, which is now purely a
// CONSEQUENCE of a declared range — and cardinality) is schema-driven: with
// no schema at all, there's nothing to check task data against, not even a
// blanket "impact must be numeric" rule (that used to hold unconditionally,
// which is exactly what wrongly rejected an ENUM value like
// "triage:impact=compatibility" — a non-numeric value is only ever a defect
// against a target the schema itself declared a range for).
func TestLint_NilSchemaSkipsEverything(t *testing.T) {
	all := []tasks.Task{
		{HarpID: "a", Tags: []string{"triage:type=anything", `triage:impact="oops"`}},
	}

	result, err := Lint(all, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

// TestLint_NonNumericValueOnEnumOnlyTargetIsNotFlagged is the regression
// test for the exact bug that motivated dropping the old unconditional
// "impact must parse as a number" check: a target declared an ENUM (never a
// range) whose value is a legitimate non-numeric vocabulary word (e.g.
// "compatibility") must NOT be flagged as "does not parse as a number" — the
// numeric-parse check only ever applies to a target the schema declared a
// RANGE for.
func TestLint_NonNumericValueOnEnumOnlyTargetIsNotFlagged(t *testing.T) {
	schema := mustSchema(t, `tagma.enum:"triage:impact"="correctness,security,compatibility"`)
	all := []tasks.Task{
		{HarpID: "a", Tags: []string{"triage:impact=compatibility"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

// TestLint_FormulaEnumRef_FlagsValueNotInDeclaredEnum proves a
// value-qualified placeholder in a declared formula — e.g.
// "{{triage:impact=foo}}" — is checked against triage:impact's OWN declared
// enum: a value that isn't a member is a SCHEMA-level violation (never
// silently inert), tagged with SchemaViolationHarpID rather than any task's
// harp ID.
func TestLint_FormulaEnumRef_FlagsValueNotInDeclaredEnum(t *testing.T) {
	schema := mustSchema(t,
		`tagma.enum:"triage:impact"="correctness,security"`,
		`tagma.decay_fn:"triage:severity"="{{triage:impact=nonexistent}} > 0 ? 2 : 1"`,
	)

	result, err := Lint(nil, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, SchemaViolationHarpID, result.Violations[0].HarpID)
	assert.Contains(t, result.Violations[0].Reason, "triage:impact")
	assert.Contains(t, result.Violations[0].Reason, "nonexistent")
}

// TestLint_FormulaEnumRef_AllowsDeclaredValue is the negative case: a
// composite placeholder referencing an actual enum member raises nothing.
func TestLint_FormulaEnumRef_AllowsDeclaredValue(t *testing.T) {
	schema := mustSchema(t,
		`tagma.enum:"triage:impact"="correctness,security"`,
		`tagma.decay_fn:"triage:severity"="{{triage:impact=security}} > 0 ? 2 : 1"`,
	)

	result, err := Lint(nil, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

// TestLint_FormulaEnumRef_SkipsWhenReferencedTargetHasNoEnum proves the
// check only fires when the REFERENCED target itself declares an enum —
// nothing to validate a composite reference against otherwise.
func TestLint_FormulaEnumRef_SkipsWhenReferencedTargetHasNoEnum(t *testing.T) {
	schema := mustSchema(t,
		`tagma.decay_fn:"triage:severity"="{{triage:impact=whatever}} > 0 ? 2 : 1"`,
	)

	result, err := Lint(nil, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

// TestLint_FormulaEnumRef_SkipsUniversalPresenceWildcard proves a
// "{{ns:key=*}}" placeholder (the universal presence test — see
// internal/shared/tasks/priority's resolveTagValues "target=*" composite
// key) is never flagged as an invalid enum reference, even when the target
// DOES declare an enum: "*" is reserved syntax for "any value, or none",
// never a literal enum member (and tagma's own tag-value charset already
// rejects a literal "*" in real task data, so this can never collide with a
// legitimate enum member either).
func TestLint_FormulaEnumRef_SkipsUniversalPresenceWildcard(t *testing.T) {
	schema := mustSchema(t,
		`tagma.enum:"triage:exposed"="wire,cli,config,on-disk,api"`,
		`tagma.decay_fn:"triage:kind"="{{triage:exposed=*}} > 0 ? 2 : 1"`,
	)

	result, err := Lint(nil, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations, "the universal presence wildcard must never be checked against a declared enum")
}

func TestLint_MalformedRangeDeclarationIsAReturnedError(t *testing.T) {
	schema := mustSchema(t, `tagma.range:"triage:impact"="not-a-range"`)
	_, err := Lint(nil, schema)
	require.Error(t, err)
}

func TestLint_ViolationsAreSortedForDeterministicOutput(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "zzz", Tags: []string{"triage:type=nope"}},
		{HarpID: "aaa", Tags: []string{"triage:type=nope"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 2)
	assert.Equal(t, "aaa", result.Violations[0].HarpID)
	assert.Equal(t, "zzz", result.Violations[1].HarpID)
}

// TestLint_FlagsNaNAsOutOfRange pins U121-F08: strconv.ParseFloat accepts
// the literal string "NaN" (a legal tagma bare token), and every `<`/`>`
// bounds comparison against NaN is false — so a NaN value passed both
// branches of the range check silently, reported as conformant, on its way
// to hanging priority.rankNormalize (U124-F01) with no warning from the
// project's only read-time data-conformance sweep.
func TestLint_FlagsNaNAsOutOfRange(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "nan-value", Tags: []string{"triage:impact=NaN"}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 1, "a NaN value on a range-declared target must be flagged, not silently accepted as conformant")
	assert.Contains(t, result.Violations[0].Reason, "triage:impact")
}

// TestLint_CheckedTargetsReportsHowMuchWasExamined pins U121-F01: an empty
// Violations slice is ambiguous by itself between "genuinely clean" and
// "checked nothing because the schema declares no enum/range facet" —
// CheckedTargets must distinguish the two.
func TestLint_CheckedTargetsReportsHowMuchWasExamined(t *testing.T) {
	schema := tripleSchema(t) // declares one enum target + one range target
	all := []tasks.Task{{HarpID: "a", Tags: []string{"triage:type=security", "triage:impact=3"}}}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
	assert.Equal(t, 2, result.CheckedTargets, "triple schema declares one enum target and one range target")
}

// TestLint_NilSchemaChecksZeroTargets is the negative case this row is
// about: a nil schema returns a clean (empty) Violations slice, but
// CheckedTargets must be 0 so a caller positioning lint as a CI gate can
// tell it checked nothing rather than found nothing wrong.
func TestLint_NilSchemaChecksZeroTargets(t *testing.T) {
	all := []tasks.Task{{HarpID: "a", Tags: []string{"triage:type=anything"}}}
	result, err := Lint(all, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
	assert.Equal(t, 0, result.CheckedTargets)
}

// TestLint_ArityOnlySchemaChecksZeroTargets: an arity-only schema (no
// enum/range facet declared at all) likewise checks zero targets by this
// count — arity/scalar-cardinality is a data-driven check, not scoped by a
// declared target list the way enum/range are.
func TestLint_ArityOnlySchemaChecksZeroTargets(t *testing.T) {
	schema := mustSchema(t, `tagma.arity:"triage:type"=scalar`)
	all := []tasks.Task{{HarpID: "a", Tags: []string{"triage:type=anything-goes"}}}
	result, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
	assert.Equal(t, 0, result.CheckedTargets)
}

// TestLint_MalformedEnumDeclarationIsAReturnedError pins the CONSUMER half of
// the empty-but-present enum defect. tagschema.Enum reports a declaration with
// no usable member as an error (its own pins cover that); the property that
// matters here is what Lint does with it. Swallowing it leaves an empty member
// list, which is indistinguishable at this layer from a closed set that admits
// nothing — so every value on the target is flagged, every task in the project
// sprouts a violation, and nothing anywhere can say the DECLARATION is what is
// broken. The malformed range declaration above is already held to exactly
// this standard; enum must match it.
func TestLint_MalformedEnumDeclarationIsAReturnedError(t *testing.T) {
	for _, decl := range []string{
		`tagma.enum:"triage:type"=","`,
		`tagma.enum:"triage:type"=""`,
	} {
		t.Run(decl, func(t *testing.T) {
			schema := mustSchema(t, decl)
			all := []tasks.Task{
				{HarpID: "aaa", Tags: []string{"triage:type=correctness"}},
				{HarpID: "bbb", Tags: []string{"triage:type=security"}},
			}

			result, err := Lint(all, schema)
			require.Error(t, err, "a declaration with no usable member is a config defect Lint must report")
			assert.Contains(t, err.Error(), "triage:type", "the error must name the offending target")
			assert.Empty(t, result.Violations,
				"the tasks are not at fault; blaming their values hides the broken declaration")
		})
	}
}

// TestLint_SameValueSpelledTwoWaysIsNotAScalarViolation pins the false
// positive. groupByTarget collected values without deduping, so two DIFFERENT
// raw tag strings that parse to the SAME (target, value) — tagma accepts a
// value bare or quoted, and `triage:type=docs` and `triage:type="docs"` are
// the same tag — landed as two entries. The scalar check counts entries, so it
// reported a task carrying one value as carrying two, in a message that
// contradicted itself: "2 distinct values [docs docs]".
//
// This is the mirror of the check's real purpose. arity=scalar means at most
// one VALUE per target, and one value written two ways is still one value; a
// lint that cannot tell those apart cannot be trusted about the case it exists
// for, which is genuinely divergent values in foreign or hand-edited log data.
func TestLint_SameValueSpelledTwoWaysIsNotAScalarViolation(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "aaa", Tags: []string{`triage:type=docs`, `triage:type="docs"`}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, result.Violations,
		"one value spelled two ways is one value; arity=scalar is not violated")
}

// TestLint_GenuinelyDifferentValuesStillViolateScalar guards the fix against
// over-correcting: deduping must not silence the violation the check exists to
// find.
func TestLint_GenuinelyDifferentValuesStillViolateScalar(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "aaa", Tags: []string{`triage:type=docs`, `triage:type=chore`}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, result.Violations, 1)
	assert.Contains(t, result.Violations[0].Reason, "arity=scalar")
	assert.Contains(t, result.Violations[0].Reason, "2 distinct values")
}

// TestLint_DuplicateTagDoesNotProduceDuplicateViolations pins the other face of
// the same defect: with values ungrouped, a repeated tag produced one
// violation PER OCCURRENCE, so the same finding was reported twice about the
// same task. The sort at the end orders violations but has never deduped them.
func TestLint_DuplicateTagDoesNotProduceDuplicateViolations(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "aaa", Tags: []string{`triage:type=nope`, `triage:type="nope"`}},
	}

	result, err := Lint(all, schema)
	require.NoError(t, err)
	// One bad value, so exactly one enum violation — and no scalar violation,
	// because it is one value spelled twice.
	require.Len(t, result.Violations, 1)
	assert.Contains(t, result.Violations[0].Reason, "not one of the declared enum values")
}
