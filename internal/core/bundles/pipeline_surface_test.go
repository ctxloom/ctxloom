package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE AGENT RECEIVES THE BODY. The process stage (Pipeline.deliver) serves a
// fragment's body and carries its premise beside it, never the framed surface.

func premisedFragment() (BundleFragment, map[string]*Bundle) {
	frag := BundleFragment{
		ItemBody: ItemBody{Content: "RAW-BODY-MARKER"},
		Premise:  "you are about to remove a worktree",
	}
	return frag, map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{"f": frag}}}
}

func TestPipeline_ServesTheBodyAndCarriesThePremise(t *testing.T) {
	_, seed := premisedFragment()

	got, err := admitAllPipe(NewLoader(seedLocal(seed)), false).GetFragment("b#fragments/f")
	require.NoError(t, err)
	assert.Equal(t, "RAW-BODY-MARKER", got.Content, "the agent receives the body, never the frame")
	assert.Equal(t, "you are about to remove a worktree", got.Premise)
	assert.Equal(t, FormRaw, got.Form)
}

// A command has the same two-sided shape: the gate sees the framed surface
// (description, exports and body, on the command contract) while the agent is
// served the body and handed the exports as slash-command metadata. Every one
// of those reaches the agent, so every one is under the approval.

func describedCommand() (BundleCommand, map[string]*Bundle) {
	cmd := BundleCommand{
		ItemBody:    ItemBody{Content: "COMMAND-BODY"},
		Description: "install package X",
		Exports:     EngineBlocks{"claude-code": []byte(`{"allowed_tools":["Bash(apt-get:*)"]}`)},
	}
	return cmd, map[string]*Bundle{"b": {Name: "b", Commands: map[string]BundleCommand{"c": cmd}}}
}

func TestPipeline_CommandServesTheBody(t *testing.T) {
	cmd, seed := describedCommand()

	got, err := admitAllPipe(NewLoader(seedLocal(seed)), false).GetCommand("b#commands/c")
	require.NoError(t, err)
	assert.Equal(t, "COMMAND-BODY", got.Content, "the agent receives the body, never the frame")
	assert.Equal(t, cmd.Exports, got.Exports)
	assert.Equal(t, FormRaw, got.Form)
}
