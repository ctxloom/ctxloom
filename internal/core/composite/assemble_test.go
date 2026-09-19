package composite_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// selectAlpha is the selection most assembly tests start from: alpha's
// fragments in bundle order with the project variable bound.
func selectAlpha(t *testing.T, cat bundles.Catalog, extra ...profiles.ResolvedProfile) composite.Selection {
	t.Helper()
	p := profiles.ResolvedProfile{Bundles: []string{"alpha"}, Variables: map[string]string{"project": "golden"}}
	sel, err := composite.Select(append([]profiles.ResolvedProfile{p}, extra...), cat, composite.SelectRequest{})
	require.NoError(t, err)
	return sel
}

func itemRefs[T any](items []composite.Item[T]) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Ref)
	}
	return out
}

// An Ungated trust is for listing surfaces; it can never assemble.
func TestAssemble_RefusesAnUngatedTrust(t *testing.T) {
	cat := corpus(t)
	_, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), composite.Ungated(), composite.Options{})
	assert.ErrorIs(t, err, composite.ErrUngatedAssembly)
}

// The context is the selected fragments in selection order, each with the
// profile variables substituted, joined by the section separator; a premised
// fragment is held back for the catalog, not written.
func TestAssemble_ContextIsTheSelectionInOrderSubstituted(t *testing.T) {
	cat := corpus(t)
	pkg, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), compositetest.Trust(), composite.Options{})
	require.NoError(t, err)

	assert.Equal(t, "Rules for golden.\n\n---\n\nPrefer small functions.", pkg.Context.Text)
	assert.NotEmpty(t, pkg.Context.Hash)
	assert.Equal(t, []string{alphaRules, alphaStyle}, itemRefs(pkg.Fragments))
	assert.Equal(t, []string{alphaMaybe}, itemRefs(pkg.Premised))
	assert.Equal(t, "the agent is about to touch a signed bundle", pkg.Premised[0].Value.Premise)
	assert.Equal(t, trust.Allow, pkg.Fragments[0].Decision)
	assert.Equal(t, bundles.FormRaw, pkg.Fragments[0].Form)
}

// A premised fragment the caller named loads (naming it is the selection);
// a static assembly writes every premised fragment because nothing behind
// the surface can pull one later.
func TestAssemble_PremisedFragmentsLoadWhenExplicitOrStatic(t *testing.T) {
	cat := corpus(t)

	sel, err := composite.Select(nil, cat, composite.SelectRequest{Fragments: []string{"maybe"}})
	require.NoError(t, err)
	pkg, err := composite.Assemble(context.Background(), cat, sel, compositetest.Trust(), composite.Options{})
	require.NoError(t, err)
	assert.Equal(t, []string{alphaMaybe}, itemRefs(pkg.Fragments))
	assert.Empty(t, pkg.Premised)

	static, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), compositetest.Trust(), composite.Options{Static: true})
	require.NoError(t, err)
	assert.Equal(t, []string{alphaRules, alphaStyle, alphaMaybe}, itemRefs(static.Fragments))
	assert.Empty(t, static.Premised)
	assert.Contains(t, static.Context.Text, "Do not edit a signed bundle in place.")
}

// A withheld required item refuses the assembly unless the caller accepts
// the loss; either way the attestation names what was withheld.
func TestAssemble_WithheldItemRefusesUnlessDropped(t *testing.T) {
	cat := corpus(t)
	tr := compositetest.Trust(compositetest.RejectWhen(func(ref trust.Ref, _ []byte) bool { return ref.Name == "style" }))

	_, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), tr, composite.Options{})
	assert.ErrorIs(t, err, composite.ErrItemWithheld)

	pkg, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), tr, composite.Options{DropWithheld: true})
	require.NoError(t, err)
	assert.Equal(t, []string{alphaRules}, itemRefs(pkg.Fragments))
	assert.Equal(t, []string{alphaStyle}, pkg.Attestation().Withheld)
	assert.NotContains(t, pkg.Context.Text, "Prefer small functions.")
}

// The same item reaching the context twice — selected by ref and injected
// as a builtin under another spelling — is assembled once, first occurrence
// kept, and the drop is a finding the surface can voice.
func TestAssemble_DuplicateContentUnderTwoRefsIsAssembledOnce(t *testing.T) {
	cat := corpus(t)
	injected := composite.Fragment{Name: "ctxloom+builtin:alpha#fragments/style", Body: "Prefer small functions."}
	pkg, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), compositetest.Trust(), composite.Options{
		Builtin: []composite.Fragment{injected, {Name: "ctxloom+builtin:tools#fragments/axes", Body: "Axes."}},
	})
	require.NoError(t, err)

	assert.Equal(t, "Rules for golden.\n\n---\n\nPrefer small functions.\n\n---\n\nAxes.", pkg.Context.Text)
	assert.Equal(t, []string{alphaRules, alphaStyle, "ctxloom+builtin:tools#fragments/axes"}, itemRefs(pkg.Fragments))
	require.Len(t, pkg.Findings, 1)
	assert.Equal(t, composite.FindingDuplicate, pkg.Findings[0].Kind)
	assert.Equal(t, injected.Name, pkg.Findings[0].Ref)
}

// A fragment ask that does not load is a finding, never a refusal: the
// rest of the selection still assembles.
func TestAssemble_AnAskThatDoesNotLoadIsAFinding(t *testing.T) {
	cat := corpus(t)
	p := profiles.ResolvedProfile{Fragments: []profiles.FragmentRef{{Name: "alpha#fragments/style"}, {Name: "alpha#fragments/nope"}}}
	sel, err := composite.Select([]profiles.ResolvedProfile{p}, cat, composite.SelectRequest{})
	require.NoError(t, err)
	pkg, err := composite.Assemble(context.Background(), cat, sel, compositetest.Trust(), composite.Options{})
	require.NoError(t, err)

	assert.Equal(t, []string{alphaStyle}, itemRefs(pkg.Fragments))
	require.Len(t, pkg.Findings, 1)
	assert.Equal(t, composite.FindingLoadFailed, pkg.Findings[0].Kind)
	assert.Equal(t, alphaRef+"#fragments/nope", pkg.Findings[0].Ref)
}

// Uncurated: every command the selection's bundles ship exports, after the
// injected ones, one per item; a bundle's opt-out block rides opaque.
// Curated: only the named asks, marked Curated so an engine exports them
// even where the block opts out.
func TestAssemble_CommandsUncuratedFromBundlesOrCuratedByAsk(t *testing.T) {
	cat := corpus(t)
	own := composite.Command{Name: "init", Item: "init", Body: "Set up."}

	sel := selectAlpha(t, cat, profiles.ResolvedProfile{Bundles: []string{"beta"}})
	pkg, err := composite.Assemble(context.Background(), cat, sel, compositetest.Trust(), composite.Options{Commands: []composite.Command{own}})
	require.NoError(t, err)
	assert.Equal(t, []string{"init", alphaRelease, alphaReview, betaShip}, itemRefs(pkg.Commands))
	assert.False(t, pkg.Commands[1].Value.Curated)
	assert.JSONEq(t, `{"enabled":false}`, string(pkg.Commands[1].Value.Exports["claude-code"]))

	curated := profiles.ResolvedProfile{Commands: []string{"alpha#commands/release"}}
	sel, err = composite.Select([]profiles.ResolvedProfile{curated}, cat, composite.SelectRequest{})
	require.NoError(t, err)
	pkg, err = composite.Assemble(context.Background(), cat, sel, compositetest.Trust(), composite.Options{Commands: []composite.Command{own}})
	require.NoError(t, err)
	assert.Equal(t, []string{"init", alphaRelease}, itemRefs(pkg.Commands))
	assert.True(t, pkg.Commands[1].Value.Curated)
	assert.Equal(t, "Prepare the release notes.", pkg.Commands[1].Value.Body)
}

// EngineItems hands an engine ITS block and nothing of any other engine's;
// an item with no block for the engine carries none.
func TestAssemble_EngineItemsCarriesOnlyThatEnginesBlock(t *testing.T) {
	cat := corpus(t)
	pkg, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), compositetest.Trust(), composite.Options{})
	require.NoError(t, err)

	items := pkg.EngineItems(engine.Name("claude-code"))
	require.Len(t, items.Commands, 2)
	byName := map[string]engine.CommandItem{}
	for _, c := range items.Commands {
		byName[c.Name] = c
	}
	var review map[string]any
	require.NoError(t, json.Unmarshal(byName[alphaReview].Exports, &review))
	assert.Equal(t, "Review (claude)", review["description"])
	assert.Equal(t, []any{"Read"}, review["allowed_tools"])
	assert.JSONEq(t, `{"enabled":false}`, string(byName[alphaRelease].Exports))

	other := pkg.EngineItems(engine.Name("other-engine"))
	for _, c := range other.Commands {
		assert.Nil(t, c.Exports, "no block for this engine ⇒ none handed over")
	}
	assert.Equal(t, []string{alphaRules, alphaStyle}, func() []string {
		var out []string
		for _, f := range items.Fragments {
			out = append(out, f.Ref)
		}
		return out
	}())
	assert.Equal(t, "the agent is about to touch a signed bundle", items.Fragments[0].Premise+items.Fragments[1].Premise+premiseOf(items, alphaMaybe))
}

func premiseOf(items engine.Items, ref string) string {
	for _, f := range items.Fragments {
		if f.Ref == ref {
			return f.Premise
		}
	}
	return ""
}

// The attestation has one row per delivered item — fragments, premised
// fragments, commands — each with its decision and content hash.
func TestAssemble_AttestationHasOneRowPerDeliveredItem(t *testing.T) {
	cat := corpus(t)
	pkg, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), compositetest.Trust(), composite.Options{})
	require.NoError(t, err)

	att := pkg.Attestation()
	var refs []string
	for _, row := range att.Items {
		refs = append(refs, row.Ref)
		assert.Equal(t, trust.Allow, row.Decision)
		assert.NotEmpty(t, row.Hash, row.Ref)
	}
	assert.ElementsMatch(t, []string{alphaRules, alphaStyle, alphaMaybe, alphaReview, alphaRelease}, refs)
	assert.Empty(t, att.Withheld)
}

// The surfaces the caller resolved ride the package unchanged, and the
// selection it was assembled from is on it for the consumers that report it.
func TestAssemble_CarriesTheResolvedSurfacesAndTheSelection(t *testing.T) {
	cat := corpus(t)
	sel := selectAlpha(t, cat, profiles.ResolvedProfile{LLM: "fast", DenyTools: []string{"Task"}})
	pkg, err := composite.Assemble(context.Background(), cat, sel, compositetest.Trust(), composite.Options{DenyTools: []string{"Task"}, Statusline: true})
	require.NoError(t, err)

	assert.Equal(t, []string{"Task"}, pkg.DenyTools)
	assert.True(t, pkg.Statusline)
	assert.Equal(t, "fast", pkg.Selection.LLM)
}

// The index enumerates the CATALOG — every item, with its kind, description
// and premise — not the selection.
func TestIndexOf_EnumeratesTheCatalog(t *testing.T) {
	idx, err := composite.IndexOf(corpus(t))
	require.NoError(t, err)

	byRef := map[string]composite.IndexEntry{}
	for _, e := range idx.Entries {
		byRef[e.Ref] = e
	}
	assert.Equal(t, trust.KindFragment, byRef[alphaMaybe].Kind)
	assert.Equal(t, "the agent is about to touch a signed bundle", byRef[alphaMaybe].Premise)
	assert.Equal(t, trust.KindPrompt, byRef[alphaReview].Kind)
	assert.Equal(t, "Review the diff", byRef[alphaReview].Description)
	assert.Contains(t, byRef, betaShip)
	assert.Contains(t, byRef, betaTagged)
}
