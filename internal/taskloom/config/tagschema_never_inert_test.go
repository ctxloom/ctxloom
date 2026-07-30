package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// tagschema.Parse on an empty declaration list returns a valid, EMPTY Schema
// and a nil error — an object on which every schema-driven behaviour (scalar
// collapse, enum/range validation, priority_fn/decay_fn) is inert. What
// stops that reaching the product is this seam, not tagschema: ParsedTagSchema
// parses ResolvedTagSchema, which falls back to DefaultTagSchema whenever the
// configured list is empty. Parse therefore never sees an empty list from any
// production path.
//
// This pins that guarantee at the seam that makes it true. Drop the fallback
// and the inert schema becomes reachable.
func TestParsedTagSchemaIsNeverInert(t *testing.T) {
	cases := map[string]Config{
		"tag_schema absent":         {},
		"tag_schema explicitly nil": {TagSchema: nil},
		"tag_schema explicitly []":  {TagSchema: []string{}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, cfg.ResolvedTagSchema(),
				"an unset tag_schema must resolve to DefaultTagSchema, never to nothing")

			s, err := cfg.ParsedTagSchema()
			require.NoError(t, err)

			// Every facet the product drives off must be declared.
			for _, facet := range []string{
				tagschema.ArityFacet,
				tagschema.PriorityFnFacet,
				tagschema.DecayFnFacet,
				tagschema.EnumFacet,
			} {
				assert.NotEmpty(t, s.Targets(facet),
					"facet %q declares no target — the schema is inert", facet)
			}
		})
	}
}
