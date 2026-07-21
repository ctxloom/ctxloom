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

func TestLint_NilSchemaSkipsEnumRangeCardinalityButStillCatchesNonNumericImpact(t *testing.T) {
	all := []tasks.Task{
		{HarpID: "a", Tags: []string{"triage:type=anything", `triage:impact="oops"`}},
	}

	violations, err := Lint(all, nil)
	require.NoError(t, err)
	require.Len(t, violations, 1, "a non-numeric impact is always a defect, schema or not")
	assert.Contains(t, violations[0].Reason, "does not parse as a number")
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
