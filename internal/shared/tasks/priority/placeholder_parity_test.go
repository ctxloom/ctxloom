package priority

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// TestReferencedTagTargets_SeesPlaceholders pins that this package still
// resolves a formula's placeholder references through tagschema, the owner of
// the syntax. A value qualifier is stripped; a builtin is not a tag.
func TestReferencedTagTargets_SeesPlaceholders(t *testing.T) {
	f, err := tagschema.CompileFormula("{{triage:impact=capability}} + {{b:c}} + {{age_days}}")
	require.NoError(t, err)

	assert.Equal(t, map[string]bool{"triage:impact": true, "b:c": true}, referencedTagTargets(f, nil))
}

// TestCheckKnownBuiltins_SeesPlaceholders pins the same dependency on the
// diagnostic that fails loud: if the extraction stops matching, every
// made-up builtin name passes.
func TestCheckKnownBuiltins_SeesPlaceholders(t *testing.T) {
	bad, err := tagschema.CompileFormula("{{not_a_builtin}}")
	require.NoError(t, err)
	err = checkKnownBuiltins(bad, tagschema.PriorityFnFacet, "triage:score")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not_a_builtin")

	good, err := tagschema.CompileFormula("{{age_days}} + {{triage:impact}}")
	require.NoError(t, err)
	assert.NoError(t, checkKnownBuiltins(good, tagschema.PriorityFnFacet, "triage:score"))
}
