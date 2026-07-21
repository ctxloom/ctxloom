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

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, violations)
}

func TestLint_FlagsTypeNotInEnum(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "bad-type", Tags: []string{"triage:type=sparkles"}},
	}

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, violations, 1)
	assert.Equal(t, "bad-type", violations[0].HarpID)
	assert.Contains(t, violations[0].Reason, "triage:type")
	assert.Contains(t, violations[0].Reason, "sparkles")
}

func TestLint_FlagsImpactOutOfRange(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "too-high", Tags: []string{"triage:impact=9"}},
	}

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, violations, 1)
	assert.Equal(t, "too-high", violations[0].HarpID)
	assert.Contains(t, violations[0].Reason, "outside the declared range")
}

func TestLint_FlagsImpactThatDoesNotParseAsANumber(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "non-numeric", Tags: []string{`triage:impact="not-a-number"`}},
	}

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, violations, 1)
	assert.Contains(t, violations[0].Reason, "does not parse as a number")
}

// TestLint_FlagsScalarCardinalityViolation covers the "foreign/legacy data"
// case the write-seam collapse should prevent going forward: two distinct
// values for the same arity=scalar target on one task.
func TestLint_FlagsScalarCardinalityViolation(t *testing.T) {
	schema := tripleSchema(t)
	all := []tasks.Task{
		{HarpID: "double-typed", Tags: []string{"triage:type=security", "triage:type=docs"}},
	}

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	// Both a distinct-value-count violation AND (if either value isn't in
	// the enum) an enum violation could fire; here both values ARE valid
	// enum members, so only the cardinality violation should appear.
	require.Len(t, violations, 1)
	assert.Contains(t, violations[0].Reason, "arity=scalar")
	assert.Contains(t, violations[0].Reason, "2 distinct values")
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

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, violations)
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

	violations, err := Lint(all, nil)
	require.NoError(t, err)
	assert.Empty(t, violations)
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

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	assert.Empty(t, violations)
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

	violations, err := Lint(nil, schema)
	require.NoError(t, err)
	require.Len(t, violations, 1)
	assert.Equal(t, SchemaViolationHarpID, violations[0].HarpID)
	assert.Contains(t, violations[0].Reason, "triage:impact")
	assert.Contains(t, violations[0].Reason, "nonexistent")
}

// TestLint_FormulaEnumRef_AllowsDeclaredValue is the negative case: a
// composite placeholder referencing an actual enum member raises nothing.
func TestLint_FormulaEnumRef_AllowsDeclaredValue(t *testing.T) {
	schema := mustSchema(t,
		`tagma.enum:"triage:impact"="correctness,security"`,
		`tagma.decay_fn:"triage:severity"="{{triage:impact=security}} > 0 ? 2 : 1"`,
	)

	violations, err := Lint(nil, schema)
	require.NoError(t, err)
	assert.Empty(t, violations)
}

// TestLint_FormulaEnumRef_SkipsWhenReferencedTargetHasNoEnum proves the
// check only fires when the REFERENCED target itself declares an enum —
// nothing to validate a composite reference against otherwise.
func TestLint_FormulaEnumRef_SkipsWhenReferencedTargetHasNoEnum(t *testing.T) {
	schema := mustSchema(t,
		`tagma.decay_fn:"triage:severity"="{{triage:impact=whatever}} > 0 ? 2 : 1"`,
	)

	violations, err := Lint(nil, schema)
	require.NoError(t, err)
	assert.Empty(t, violations)
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

	violations, err := Lint(all, schema)
	require.NoError(t, err)
	require.Len(t, violations, 2)
	assert.Equal(t, "aaa", violations[0].HarpID)
	assert.Equal(t, "zzz", violations[1].HarpID)
}
