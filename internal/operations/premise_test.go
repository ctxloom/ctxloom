package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// selProfileKey addresses the fixture bundle's profile the same way a user
// would.
const selProfileKey = "ctxloom:local@bundles/sel#profiles/p1"

// writeSelectionFixture writes a bundle shipping three fragments and a profile
// composing it. premised decides whether the third fragment carries a premise;
// EVERYTHING ELSE about the two files is identical, which is what lets a test
// attribute a byte difference in the assembled context to the premise and to
// nothing else.
func writeSelectionFixture(t *testing.T, root string, premised bool) {
	t.Helper()
	bundleDir := filepath.Join(root, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))

	premise := ""
	if premised {
		premise = "    premise: \"You are about to remove a worktree.\"\n"
	}
	selYAML := `version: "1.0.0"
fragments:
  alpha:
    content: "ALPHA-BODY"
  beta:
    content: "BETA-BODY"
  gamma:
` + premise + `    content: "GAMMA-BODY"
profiles:
  p1:
    description: "selection fixture"
    bundles:
      - ctxloom:local@bundles/sel
`
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "sel.yaml"), []byte(selYAML), 0o644))
}

func selectionConfig(root string) *config.Config {
	return config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join(root, ".ctxloom")}})
}

func assembleSelection(t *testing.T, premised bool, req AssembleContextRequest) *AssembleContextResult {
	t.Helper()
	root := t.TempDir()
	writeSelectionFixture(t, root, premised)
	res, err := AssembleContext(context.Background(), selectionConfig(root), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

// TestAssembleContext_PremiselessCorpusIsByteIdentical is THE safety test for
// the premise mechanism, and it pins BYTES rather than a property.
//
// The promise it enforces: a corpus that authors no premise anywhere assembles
// exactly what it assembled before premises existed. Every fragment loads,
// nothing is withheld, and the index is empty. Absence of a premise is the
// assertion "this applies unconditionally" — so the ~93 fragments nobody ever
// touches must be unable to notice that this feature shipped.
//
// The expected value is written out in full, not derived from the same
// constants the production path joins with. A test that rebuilt the string
// through contextSectionSeparator would keep agreeing with the code after
// someone changed the separator, which is the one thing it exists to catch.
func TestAssembleContext_PremiselessCorpusIsByteIdentical(t *testing.T) {
	res := assembleSelection(t, false, AssembleContextRequest{Profile: selProfileKey})

	// A PREFIX, because always-on builtin fragments are appended after the
	// profile's own and their bodies are not this test's subject. The prefix
	// itself is exact: three bodies, in the order the bookend sort places
	// them, joined by the section separator, with nothing added or removed.
	assert.True(t, strings.HasPrefix(res.Context, "ALPHA-BODY\n\n---\n\nGAMMA-BODY\n\n---\n\nBETA-BODY"),
		"a corpus with no premise must assemble byte-identically: every fragment, in bookend order, nothing withheld.\ngot: %q", res.Context)
	assert.Empty(t, res.PremiseIndex,
		"nothing carries a premise, so there is nothing to offer and the index must be empty")
	// Named rather than counted: always-on builtin fragments load alongside
	// the profile's own, so a count would be asserting the builtin set's size
	// and would go red the next time one is added.
	for _, name := range []string{"alpha", "beta", "gamma"} {
		assert.Contains(t, res.FragmentsLoaded, "ctxloom+local:sel#fragments/"+name,
			"every fragment loads when none carries a premise")
	}
}

// TestAssembleContext_PremisedFragmentIsWithheldAndIndexed proves the other
// arm: a fragment that DOES carry a premise is kept out of the assembled bytes
// and offered in the index instead.
//
// It asserts the withheld body is ABSENT from the context, which is the whole
// economic point — an index that listed the fragment while still shipping its
// body would cost more than loading it and would pass a weaker test.
func TestAssembleContext_PremisedFragmentIsWithheldAndIndexed(t *testing.T) {
	res := assembleSelection(t, true, AssembleContextRequest{Profile: selProfileKey})

	assert.True(t, strings.HasPrefix(res.Context, "ALPHA-BODY\n\n---\n\nBETA-BODY"),
		"the two unconditional fragments assemble unchanged and adjacent — the withheld one left no gap.\ngot: %q", res.Context)
	assert.NotContains(t, res.Context, "GAMMA-BODY", "withheld means the body is not paid for")
	assert.NotContains(t, res.FragmentsLoaded, "ctxloom+local:sel#fragments/gamma",
		"a withheld fragment must not be reported as loaded")

	require.Len(t, res.PremiseIndex, 1, "the premised fragment is offered")
	assert.Equal(t, "You are about to remove a worktree.", res.PremiseIndex[0].Premise,
		"the index carries the authored premise verbatim — it is what the agent decides on")
	assert.Contains(t, res.PremiseIndex[0].Name, "gamma",
		"the index names the fragment, which is the identifier a selection quotes back")
}

// TestAssembleContext_ExplicitAskLoadsPremisedFragment closes the loop: the
// name the index handed out, asked for by name, delivers the body.
//
// Without this the mechanism is a one-way withhold — guidance removed from
// context with no way to get it back — so this is the test that distinguishes
// a selection loop from a deletion.
func TestAssembleContext_ExplicitAskLoadsPremisedFragment(t *testing.T) {
	res := assembleSelection(t, true, AssembleContextRequest{
		Profile:   selProfileKey,
		Fragments: []string{"ctxloom:local@bundles/sel#fragments/gamma"},
	})

	assert.Contains(t, res.Context, "GAMMA-BODY",
		"a fragment asked for BY NAME loads despite its premise — the ask IS the selection")
	assert.Empty(t, res.MissingFragments,
		"a fragment that was asked for and delivered must not be reported missing")
	assert.Empty(t, res.PremiseIndex,
		"nothing was withheld, so nothing is offered back")
}

// TestAssembleContext_UnknownAskIsReportedMissing pins the silent-no-op guard
// on the selection leg. A selection that names a fragment which does not
// resolve must SAY SO: an agent that asked for guidance and silently received
// none would proceed believing it had been told the rules.
func TestAssembleContext_UnknownAskIsReportedMissing(t *testing.T) {
	res := assembleSelection(t, true, AssembleContextRequest{
		Profile:   selProfileKey,
		Fragments: []string{"ctxloom:local@bundles/sel#fragments/nosuch"},
	})

	// The CANONICAL spelling: an ask is resolved to its qualified pipeline
	// name at intake, and that is the name reported back.
	assert.Contains(t, res.MissingFragments, "ctxloom+local:sel#fragments/nosuch",
		"a requested fragment that did not load must be named in MissingFragments")
}

// TestPremiseIndex_ListsOnlyPremisedFragments covers the corpus-wide producer,
// which answers a different question from the assembly-scoped index: what
// exists to be selected, rather than what this assembly withheld.
func TestPremiseIndex_ListsOnlyPremisedFragments(t *testing.T) {
	root := t.TempDir()
	writeSelectionFixture(t, root, true)
	cfg := selectionConfig(root)

	entries, err := PremiseIndex(cfg.BundleLoader().Catalog())
	require.NoError(t, err)

	require.Len(t, entries, 1, "only the premised fragment is indexed; unconditional ones are not decisions")
	assert.Equal(t, "gamma", entries[0].Name)
	assert.Equal(t, "You are about to remove a worktree.", entries[0].Premise)
}

// TestRenderPremiseIndex_EmptyRendersNothing pins the property the
// byte-identity promise rests on at the rendering layer: no entries renders no
// bytes, not a heading over an empty list.
func TestRenderPremiseIndex_EmptyRendersNothing(t *testing.T) {
	assert.Equal(t, "", RenderPremiseIndex(nil))
	assert.Equal(t, "", RenderPremiseIndex([]PremiseIndexEntry{}))
}

// TestRenderPremiseIndex_CarriesNameAndPremiseOnly pins what a row costs. The
// index is paid for on every session while the bodies it advertises are not,
// so a row growing a body is the failure that would defeat the mechanism.
func TestRenderPremiseIndex_CarriesNameAndPremiseOnly(t *testing.T) {
	out := RenderPremiseIndex([]PremiseIndexEntry{
		{Name: "worktree-lifecycle", Premise: "You are about to remove a worktree."},
	})

	assert.Contains(t, out, "worktree-lifecycle")
	assert.Contains(t, out, "You are about to remove a worktree.")
	assert.Contains(t, out, "assemble_context",
		"the index must tell the agent HOW to ask, or a correct selection has nowhere to go")
}
