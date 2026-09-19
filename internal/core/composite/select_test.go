package composite_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

func names(asks []composite.FragmentAsk) []string {
	out := make([]string, 0, len(asks))
	for _, a := range asks {
		out = append(out, a.Name)
	}
	return out
}

// Select's fragment order IS the assembly order: one entry per item (the
// highest priority any ask gave it wins), a whole-bundle ask expanding to
// that bundle's fragments by name, and the bookend placement — highest
// priority first, second-highest last, the rest between in ask order.
func TestSelect_FragmentsAreOneEntryPerItemBookendedByPriority(t *testing.T) {
	cat := corpus(t)
	p := profiles.ResolvedProfile{
		Bundles: []string{"alpha"},
		Fragments: []profiles.FragmentRef{
			{Name: "alpha#fragments/style", Priority: 5},
			{Name: "beta#fragments/tagged", Priority: 3},
			{Name: "alpha#fragments/rules", Priority: -1},
		},
	}
	sel, err := composite.Select([]profiles.ResolvedProfile{p}, cat, composite.SelectRequest{})
	require.NoError(t, err)

	// style(5) leads, tagged(3) is second-highest so it closes, and the
	// whole-bundle expansion's rules(0 > -1) and maybe(0) sit between.
	assert.Equal(t, []string{alphaStyle, alphaRules, alphaMaybe, betaTagged}, names(sel.Fragments))
	assert.Equal(t, 5, sel.Fragments[0].Priority)
	assert.Equal(t, 0, sel.Fragments[1].Priority, "the higher of the two asks for rules wins")
}

// A profile's exclusions drop the excluded fragment from both the direct
// asks and the bundle expansion; a "@<commit>" pin splits into Version.
func TestSelect_ExclusionsApplyAndVersionPinsSplit(t *testing.T) {
	cat := corpus(t)
	p := profiles.ResolvedProfile{
		Bundles:          []string{"alpha"},
		Fragments:        []profiles.FragmentRef{{Name: "beta@abc123#fragments/tagged"}},
		ExcludeFragments: []string{"alpha#fragments/maybe"},
	}
	sel, err := composite.Select([]profiles.ResolvedProfile{p}, cat, composite.SelectRequest{})
	require.NoError(t, err)

	assert.NotContains(t, names(sel.Fragments), alphaMaybe)
	var tagged composite.FragmentAsk
	for _, a := range sel.Fragments {
		if a.Name == betaTagged {
			tagged = a
		}
	}
	assert.Equal(t, "abc123", tagged.Version, "the pin rides in Version; the name stays version-agnostic")
}

// The caller's explicit arm: named fragments load even when premised (they
// are recorded as Explicit, resolved to their qualified identity) and tag
// asks expand to every fragment carrying the tag, by name.
func TestSelect_ExplicitAndTagAsksFoldIn(t *testing.T) {
	cat := corpus(t)
	sel, err := composite.Select(nil, cat, composite.SelectRequest{Fragments: []string{"maybe"}, Tags: []string{"house"}})
	require.NoError(t, err)

	assert.Equal(t, []string{alphaMaybe}, sel.Explicit)
	assert.ElementsMatch(t, []string{alphaMaybe, alphaRules, betaTagged}, names(sel.Fragments))
	assert.Equal(t, []string{"house"}, sel.Tags)
}

// Everything else a profile set declares rides the Selection as written:
// curated asks, the bundle set, hooks with the declaring profile's source,
// vetoed servers, deny tools, variables (later wins) and the engine label
// (first non-empty).
func TestSelect_CarriesTheProfileSetsDeclarations(t *testing.T) {
	cat := corpus(t)
	first := profiles.ResolvedProfile{
		Bundles:   []string{"alpha", "beta"},
		Commands:  []string{"alpha#commands/release"},
		Skills:    []string{"alpha#skills/reviewer"},
		Variables: map[string]string{"project": "one", "team": "core"},
		DenyTools: []string{"Task"},
		LLM:       "",
		SourceRef: "https://example.com/r@bundles/x",
		Hooks:     wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "echo one"}}}},
	}
	second := profiles.ResolvedProfile{
		Bundles:    []string{"beta"},
		Variables:  map[string]string{"project": "two"},
		DenyTools:  []string{"WebFetch", "Task"},
		ExcludeMCP: []string{"db"},
		LLM:        "fast",
	}
	sel, err := composite.Select([]profiles.ResolvedProfile{first, second}, cat, composite.SelectRequest{})
	require.NoError(t, err)

	assert.Equal(t, []composite.ItemAsk{{Ref: "alpha#commands/release"}}, sel.Commands)
	assert.Equal(t, []composite.ItemAsk{{Ref: "alpha#skills/reviewer"}}, sel.Skills)
	assert.Equal(t, []string{"alpha", "beta"}, sel.Bundles, "first occurrence kept")
	assert.Equal(t, map[string]string{"project": "two", "team": "core"}, sel.Variables)
	assert.Equal(t, []string{"Task", "WebFetch"}, sel.DenyTools)
	assert.Equal(t, "fast", sel.LLM)
	assert.Contains(t, sel.Exclusions, "db")
	require.Len(t, sel.Hooks, 1, "only the profile that declares hooks contributes an entry")
	assert.Equal(t, "https://example.com/r@bundles/x", sel.Hooks[0].SourceRef)
	assert.Equal(t, "echo one", sel.Hooks[0].Hooks.Unified.PreTool[0].Command)
}
