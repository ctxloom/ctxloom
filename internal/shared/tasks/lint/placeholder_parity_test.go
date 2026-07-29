package lint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// TestFormulaEnumRefViolations_SeesPlaceholders pins that this package still
// resolves a formula's placeholder references through tagschema, the owner of
// the syntax. If the extraction stops matching, formulaEnumRefViolations
// silently reports no violations for every schema — a passing lint that
// checked nothing.
func TestFormulaEnumRefViolations_SeesPlaceholders(t *testing.T) {
	schema, err := tagschema.Parse([]string{
		`tagma.priority_fn:"triage:score"="{{triage:impact=nope}} + {{ age_days }}"`,
	})
	require.NoError(t, err)

	enums := map[string][]string{"triage:impact": {"capability", "polish"}}
	got := formulaEnumRefViolations(schema, enums)

	require.Len(t, got, 1)
	assert.Equal(t, SchemaViolationHarpID, got[0].HarpID)
	assert.Contains(t, got[0].Reason, "triage:impact=nope")
}

// TestFormulaEnumRefViolations_AcceptsDeclaredMemberAndReservedStar covers the
// two references that must NOT be flagged: an enum member, and the reserved
// "=*" presence test which can never be a real task value.
func TestFormulaEnumRefViolations_AcceptsDeclaredMemberAndReservedStar(t *testing.T) {
	schema, err := tagschema.Parse([]string{
		`tagma.priority_fn:"triage:score"="{{triage:impact=capability}} + {{triage:impact=*}}"`,
	})
	require.NoError(t, err)

	enums := map[string][]string{"triage:impact": {"capability", "polish"}}
	assert.Empty(t, formulaEnumRefViolations(schema, enums))
}
