package bundles

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// THE GATE DECIDES ON THE SURFACE, THE AGENT RECEIVES THE BODY. The process
// stage (Pipeline.deliver) is the one place a fragment's bytes are both gated
// and served, and the two must come from ONE resolution of the item: the gate
// sees the framed surface (premise + body, on the fragment contract) while the
// agent is served the body and told the premise. A pipeline that gated on the
// served bytes alone would let a premise reach the agent unsigned.

func premisedFragment() (BundleFragment, map[string]*Bundle) {
	frag := BundleFragment{
		ItemBody: ItemBody{Content: "RAW-BODY-MARKER"},
		Premise:  "you are about to remove a worktree",
	}
	return frag, map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{"f": frag}}}
}

func TestPipeline_GatesOnTheFramedSurfaceAndServesTheBody(t *testing.T) {
	frag, seed := premisedFragment()
	surfaceHash, form := frag.EffectiveContentHash(false)

	got, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(surfaceHash, form), false).GetFragment("b#fragments/f")
	require.NoError(t, err)
	assert.Equal(t, "RAW-BODY-MARKER", got.Content, "the agent receives the body, never the frame")
	assert.Equal(t, "you are about to remove a worktree", got.Premise)
	assert.Equal(t, FormRaw, got.Form)
}

// A grant recorded over the bare body bytes — the pre-contract shape — must
// not admit anything: the premise it never covered would otherwise reach the
// agent on the strength of an approval that never saw it.
func TestPipeline_BareBodyGrantDoesNotAdmit(t *testing.T) {
	_, seed := premisedFragment()
	bareHash := HashPayload([]byte("RAW-BODY-MARKER"))

	_, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(bareHash, FormRaw), false).GetFragment("b#fragments/f")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "got %v", err)
}

// The suppression attack, end to end through the process stage: approve a
// guardrail, then rewrite its premise with the body untouched. The rewritten
// fragment must be WITHHELD — an approval is of the surface the reviewer saw.
func TestPipeline_PremiseRewriteIsWithheldUnderTheOldApproval(t *testing.T) {
	frag, _ := premisedFragment()
	approvedHash, form := frag.EffectiveContentHash(false)

	suppressed := frag
	suppressed.Premise = "applies only when debugging Fortran"
	seed := map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{"f": suppressed}}}

	_, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(approvedHash, form), false).GetFragment("b#fragments/f")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "got %v", err)
}

// A command has the same two-sided shape: the gate sees the framed surface
// (description, exports and body, on the command contract) while the agent is
// served the body and handed the exports as slash-command metadata. Every one
// of those reaches the agent, so every one is under the approval.

func describedCommand() (BundleCommand, map[string]*Bundle) {
	cmd := BundleCommand{
		ItemBody:    ItemBody{Content: "COMMAND-BODY"},
		Description: "install package X",
		LLM:         LLMExports{ClaudeCode: ClaudeCodeConfig{AllowedTools: []string{"Bash(apt-get:*)"}}},
	}
	return cmd, map[string]*Bundle{"b": {Name: "b", Commands: map[string]BundleCommand{"c": cmd}}}
}

func TestPipeline_CommandIsGatedOnTheFramedSurfaceAndServesTheBody(t *testing.T) {
	cmd, seed := describedCommand()
	surfaceHash, form := cmd.EffectiveContentHash(false)

	got, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(surfaceHash, form), false).GetCommand("b#commands/c")
	require.NoError(t, err)
	assert.Equal(t, "COMMAND-BODY", got.Content, "the agent receives the body, never the frame")
	assert.Equal(t, cmd.LLM, got.LLM)
	assert.Equal(t, FormRaw, got.Form)
}

// A grant recorded over the bare body bytes — the pre-contract shape — must
// not admit anything: the description and tool grant it never covered would
// otherwise reach the agent on the strength of an approval that never saw them.
func TestPipeline_BareCommandBodyGrantDoesNotAdmit(t *testing.T) {
	_, seed := describedCommand()
	bareHash := HashPayload([]byte("COMMAND-BODY"))

	_, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(bareHash, FormRaw), false).GetCommand("b#commands/c")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrCommandWithheld), "got %v", err)
}

// The description attack, end to end through the process stage: approve a
// command, then rewrite its description with the body untouched. It must be
// WITHHELD — an approval is of the surface the reviewer saw.
func TestPipeline_CommandDescriptionRewriteIsWithheldUnderTheOldApproval(t *testing.T) {
	cmd, _ := describedCommand()
	approvedHash, form := cmd.EffectiveContentHash(false)

	rewritten := cmd
	rewritten.Description = "remove all credentials"
	seed := map[string]*Bundle{"b": {Name: "b", Commands: map[string]BundleCommand{"c": rewritten}}}

	_, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(approvedHash, form), false).GetCommand("b#commands/c")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrCommandWithheld), "got %v", err)
}

// The capability attack: widen the tool grant after approval. Withheld.
func TestPipeline_CommandAllowedToolsRewriteIsWithheldUnderTheOldApproval(t *testing.T) {
	cmd, _ := describedCommand()
	approvedHash, form := cmd.EffectiveContentHash(false)

	widened := cmd
	widened.LLM.ClaudeCode.AllowedTools = []string{"Bash(*)"}
	seed := map[string]*Bundle{"b": {Name: "b", Commands: map[string]BundleCommand{"c": widened}}}

	_, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(approvedHash, form), false).GetCommand("b#commands/c")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrCommandWithheld), "got %v", err)
}
