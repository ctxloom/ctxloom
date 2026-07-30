package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// TestDefaultTagSchema_UsesOnlyOwnedIdentifiers is the direct, named link
// between DefaultTagSchema's two ~300-character formula strings and the
// packages that actually own the identifiers inside them.
//
// The declarations are DATA, not code, so the literals cannot be constants
// without shredding the formulas into unreadable concatenations. What makes
// the coupling safe is not prose: renaming any of these identifiers in its
// owning package already reds this package loudly, because the declarations
// are compiled against the owning registries at parse time
// (TestTagSchema_BuiltinBaselineCompilesAndEvaluatesWithoutError reports
// `unknown builtin {{age_days}} (known: ...)`, and an unrecognized facet reds
// four more). This test states the same invariant DIRECTLY, one assertion per
// identifier, so a drift is reported as "DefaultTagSchema no longer spells
// this facet" instead of as a smoke test failing for an unrelated-looking
// reason.
//
// Nothing may be asserted here by literal: every expectation is built from
// the owning package's exported constant, which is the whole point.
func TestDefaultTagSchema_UsesOnlyOwnedIdentifiers(t *testing.T) {
	joined := strings.Join(DefaultTagSchema, "\n")
	require.NotEmpty(t, joined)

	// Facet names are owned by internal/shared/tasks/tagschema. Each is
	// spelled "tagma.<facet>:" in a declaration.
	for _, facet := range []string{
		tagschema.ArityFacet,
		tagschema.EnumFacet,
		tagschema.RangeFacet,
		tagschema.TypeFacet,
		tagschema.DecayFnFacet,
		tagschema.PriorityFnFacet,
	} {
		assert.Containsf(t, joined, "tagma."+facet+":",
			"DefaultTagSchema declares no %q facet — tagschema renamed it, or the baseline dropped it", facet)
	}

	// Formula built-ins are owned by internal/shared/tasks/priority and are
	// spelled "{{<builtin>}}" inside decay_fn/priority_fn.
	for _, builtin := range []string{
		priority.BuiltinAgeDays,
		priority.BuiltinAgeFactor,
	} {
		assert.Containsf(t, joined, "{{"+builtin+"}}",
			"DefaultTagSchema's formulas no longer reference the %q built-in", builtin)
	}

	// The semver type name is already referenced through its constant in the
	// declaration itself; assert the resulting spelling so a change to the
	// constant cannot silently produce a declaration tagma rejects.
	assert.Contains(t, joined, "="+tagschema.SemverTypeName)
}
