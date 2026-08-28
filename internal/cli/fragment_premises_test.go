package cli

import (
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// renderFor is the exact rendering the command performs, exercised directly.
// The command's own body is config loading plus this branch; the branch is what
// carries a decision, and it is the one this project's characteristic bug lives
// in — exit 0, a confident message, and nothing said about what was actually
// found.
// renderFor calls the command's OWN rendering function, not a copy of it. An
// earlier version of this test reimplemented the branch, which meant it passed
// regardless of what the command actually did.
func renderFor(t *testing.T, entries []operations.PremiseIndexEntry) string {
	t.Helper()
	return renderPremiseListing(entries)
}

// An empty index must SAY it is empty. Printing an instruction to select from
// nothing would ask an agent to choose from an empty set, and printing nothing
// at all is indistinguishable from the command failing to run.
func TestFragmentPremises_EmptyIndexSaysSoRatherThanRenderingAnEmptyMenu(t *testing.T) {
	got := renderFor(t, nil)

	assert.Contains(t, got, "No fragments carry a premise",
		"an empty corpus must be reported, not rendered as an empty menu")
	assert.Contains(t, got, "loaded unconditionally",
		"and it must say WHY there is nothing to choose — absence of a premise is an assertion")
	assert.NotContains(t, got, "NOT in your context",
		"the selection instruction must not appear when there is nothing to select")
}

// A populated index must carry the qualified refs a selection quotes back, and
// the instruction that decides how well selection performs.
func TestFragmentPremises_RendersQualifiedRefsAndTheInstruction(t *testing.T) {
	got := renderFor(t, []operations.PremiseIndexEntry{
		{Name: "sel#fragments/gamma", Premise: "You are about to remove a worktree."},
	})

	require.NotEmpty(t, got)
	assert.Contains(t, got, "sel#fragments/gamma",
		"the ref is what makes the follow-up ask exact; a bare name can resolve to another bundle's fragment")
	assert.Contains(t, got, "You are about to remove a worktree.",
		"the premise is the thing being decided on and must render verbatim")
	assert.Contains(t, got, "ON ITS OWN",
		"per-premise judgement is the largest measured effect on selection quality")
	assert.NotContains(t, got, "No fragments carry a premise",
		"a populated index must not also claim emptiness")
}

// A nil index must reach a structured caller as an empty LIST, not as null. The
// command asks for a list of what is on offer; "null" makes a parser distinguish
// "no premises" from "the field was absent", which is a distinction the corpus
// does not have.
func TestFragmentPremises_EmptyIndexIsAnEmptyListNotNull(t *testing.T) {
	// buildPremiseListing, not a nil slice converted here: the earlier version of
	// this test performed the conversion itself and then asserted on its own
	// work, which tested encoding/json rather than the command.
	b, err := json.Marshal(buildPremiseListing(nil).Fragments)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(b),
		"an empty premise index serialises as an empty array; null would force every caller to special-case it")
}

// The listing is this command's OUTPUT and belongs on stdout. cobra's Print
// family writes to OutOrStderr, so the obvious cmd.Println puts a command's
// result on the error stream: `ctxloom fragment premises > file` captures
// nothing, and the defect is invisible to any test that calls the render
// function directly instead of going through cobra.
// cobra's Print family writes to OutOrSTDERR, so the obvious cmd.Println put
// this command's OUTPUT on the error stream and `ctxloom fragment premises >
// file` captured nothing.
//
// THIS MUST CAPTURE THE PROCESS STREAMS, not cmd.SetOut/SetErr buffers. cobra's
// OutOrStderr returns the OUT writer whenever SetOut has been called, so in a
// test that sets both, OutOrStdout and OutOrStderr are THE SAME BUFFER and the
// defect is structurally invisible — a mutation swapping one for the other
// passes. Two earlier versions of this test did exactly that and stayed green
// against the real bug.
func TestFragmentPremises_ListingGoesToStdoutNotStderr(t *testing.T) {
	testsupport.ProjectDir(t)

	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	rErr, wErr, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = wOut, wErr
	t.Cleanup(func() { os.Stdout, os.Stderr = oldOut, oldErr })

	// A FRESH command, never a copy of fragmentPremisesCmd: struct copying is
	// shallow, so the copy shares the package-level command's flagset and
	// registering --format on it panics the next test that touches the real one
	// ("premises flag redefined: format").
	cmd := &cobra.Command{RunE: runFragmentPremises}
	cmd.Flags().String("format", "text", "")
	require.NoError(t, cmd.Flags().Set("format", "text"))
	runErr := runFragmentPremises(cmd, nil)

	require.NoError(t, wOut.Close())
	require.NoError(t, wErr.Close())
	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)
	os.Stdout, os.Stderr = oldOut, oldErr
	require.NoError(t, runErr)

	assert.Contains(t, string(outBytes), "No fragments carry a premise",
		"the listing is this command's OUTPUT and must reach the process stdout")
	assert.NotContains(t, string(errBytes), "No fragments carry a premise",
		"it must not reach stderr, where a redirect would miss it")
}

// The structured payload must carry the SELECTION INSTRUCTION, not just the
// rows. Piped output resolves to JSON, so an agent running this command
// programmatically is the common case — and the instruction is what carries
// selection from ~0.49 recall to ~0.93. Shipping the entries alone would leave
// that consumer choosing blind, which is what happened before this test existed.
func TestFragmentPremises_StructuredPayloadCarriesTheInstruction(t *testing.T) {
	payload := buildPremiseListing([]operations.PremiseIndexEntry{
		{Name: "b#fragments/a", Premise: "You are about to do a thing."},
	})
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	require.Contains(t, got, "instruction", "a structured caller must receive the guidance, not only the rows")
	require.Contains(t, got, "fragments")

	ins, _ := got["instruction"].(string)
	for _, phrase := range []string{"ON ITS OWN", "BORDERLINE", "ABOUT TO DO"} {
		assert.Contains(t, ins, phrase,
			"the structured instruction must carry the same measured properties as the text listing; "+
				"%q is one of the three that move recall", phrase)
	}
}
