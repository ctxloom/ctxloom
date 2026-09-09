package operations

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"

	"github.com/stretchr/testify/require"
)

// A builtin or companion-loadout fragment must reach the SAME premise filter
// every other fragment does. Before this, ingestBuiltinFragments took no filter
// at all, so an authored premise was inert in the worst possible way: the
// catalog surfaced it in the premise INDEX while assembly still delivered the
// body, and the agent was offered a menu item its context already carried.
//
// The filter is exercised directly here because that is where the decision
// lives; the ingest path's job is only to consult it.
func TestBuiltinFragmentsHonourTheirPremise(t *testing.T) {
	const (
		unconditional = "ctxloom+builtin:core#fragments/always"
		premised      = "ctxloom:companion@taskloom#fragments/taskloom"
		premise       = "You are about to create, read or close a task."
	)

	t.Run("a premised builtin is withheld and indexed", func(t *testing.T) {
		f := newPremiseFilter(nil)
		require.True(t, f.withhold(premised, premise, func() string { return "body" }),
			"a companion fragment carrying a premise must be withheld like any other")

		entries := f.entries()
		require.Len(t, entries, 1)
		require.Equal(t, premised, entries[0].Name)
		require.Equal(t, premise, entries[0].Premise)
	})

	t.Run("a builtin with NO premise stays unconditional", func(t *testing.T) {
		f := newPremiseFilter(nil)
		require.False(t, f.withhold(unconditional, "", func() string { return "body" }),
			"absence of a premise asserts the fragment applies always — this is what keeps the change additive for every existing builtin")
		require.Empty(t, f.entries())
	})

	t.Run("an explicit ask overrides the premise, as for any fragment", func(t *testing.T) {
		f := newPremiseFilter([]string{premised})
		require.False(t, f.withhold(premised, premise, func() string { return "body" }),
			"an explicit ask IS the selection callback; a premise that could veto it would stop the loop closing")
	})

	t.Run("a STATIC assembly includes it, because nothing can pull later", func(t *testing.T) {
		f := newStaticPremiseFilter()
		require.False(t, f.withhold(premised, premise, func() string { return "body" }),
			"an out-of-the-loop surface loses a withheld fragment rather than deferring it")
		require.Empty(t, f.entries())
	})

	t.Run("the withheld body reaches OnWithheld for skill emission", func(t *testing.T) {
		var got []string
		f := newPremiseFilter(nil)
		f.onWithheld = func(name, p, content string) { got = append(got, name+"|"+p+"|"+content) }

		require.True(t, f.withhold(premised, premise, func() string { return "  the body  " }))
		require.Len(t, got, 1)
		require.True(t, strings.HasPrefix(got[0], premised+"|"+premise+"|"))
		require.Contains(t, got[0], "the body")
	})
}

// The INGEST path, not just the filter: this is the half that silently ignored
// premises, and bypassing the withhold call here passed every unit test until
// this existed. Asserts on what lands in the assembled context.
func TestIngestBuiltinFragments_WithholdsPremisedOnesFromContext(t *testing.T) {
	builtins := []config.BuiltinFragment{
		{Name: "ctxloom+builtin:core#fragments/always", Content: "ALWAYS-BODY"},
		{Name: "ctxloom:companion@taskloom#fragments/taskloom", Content: "TASKLOOM-BODY", Premise: "You are about to touch the task log."},
	}

	t.Run("dynamic: the premised builtin stays OUT of the assembled bytes", func(t *testing.T) {
		ingest := newContextIngest()
		filter := newPremiseFilter(nil)

		loaded := ingestBuiltinFragments(ingest, builtins, nil, filter)
		joined := ingest.join()

		require.Contains(t, joined, "ALWAYS-BODY", "an unpremised builtin is unconditional and must still be delivered")
		require.NotContains(t, joined, "TASKLOOM-BODY",
			"a premised builtin must be WITHHELD — delivering it anyway is what made the premise inert")
		require.Contains(t, loaded, "ctxloom+builtin:core#fragments/always")
		require.NotContains(t, loaded, "ctxloom:companion@taskloom#fragments/taskloom",
			"a withheld fragment must not be reported as loaded, or a caller is told it received content it did not")

		require.Len(t, filter.entries(), 1, "the withheld one becomes the agent's menu entry")
	})

	t.Run("static: both are delivered, because nothing can pull later", func(t *testing.T) {
		ingest := newContextIngest()
		loaded := ingestBuiltinFragments(ingest, builtins, nil, newStaticPremiseFilter())
		joined := ingest.join()

		require.Contains(t, joined, "ALWAYS-BODY")
		require.Contains(t, joined, "TASKLOOM-BODY", "a static surface loses a withheld fragment rather than deferring it")
		require.Len(t, loaded, 2)
	})
}
