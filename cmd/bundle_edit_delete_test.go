// Tests for applyBundleEdits (the flag-driven mutation matrix that
// runBundleEdit composes) and deleteBundleFile (the confirm-gated file
// remove behind runBundleDelete). The cobra wrappers continue to
// handle config loading and loader IO; these helpers carry the
// decision logic worth pinning.
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/iox"
)

// =============================================================================
// applyBundleEdits
// =============================================================================

func TestApplyBundleEdits_NoMutationsReturnsFalse(t *testing.T) {
	b := &bundles.Bundle{Version: "1.0", Description: "orig"}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{}, iox.NewErrWriter(&buf))

	assert.False(t, modified)
	assert.Empty(t, buf.String())
	assert.Equal(t, "1.0", b.Version, "untouched")
	assert.Equal(t, "orig", b.Description)
}

func TestApplyBundleEdits_DescriptionAndVersion(t *testing.T) {
	b := &bundles.Bundle{Version: "1.0", Description: "orig"}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		Description: "new desc",
		Version:     "2.0",
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	assert.Equal(t, "new desc", b.Description)
	assert.Equal(t, "2.0", b.Version)
}

func TestApplyBundleEdits_TagsAddDedupes(t *testing.T) {
	b := &bundles.Bundle{Tags: []string{"existing"}}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		AddTags: []string{"existing", "fresh"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified, "at least one fresh tag landed")
	assert.Equal(t, []string{"existing", "fresh"}, b.Tags)
}

func TestApplyBundleEdits_TagsAddNoFreshOnesIsNoOp(t *testing.T) {
	// Every add-tag was already present → modified should stay false.
	b := &bundles.Bundle{Tags: []string{"a", "b"}}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		AddTags: []string{"a", "b"},
	}, iox.NewErrWriter(&buf))

	assert.False(t, modified, "all-duplicate adds must not flip modified")
	assert.Equal(t, []string{"a", "b"}, b.Tags)
}

func TestApplyBundleEdits_AddFragmentCreatesPlaceholder(t *testing.T) {
	b := &bundles.Bundle{}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		AddFragments: []string{"new-frag"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	require.Contains(t, b.Fragments, "new-frag")
	assert.Contains(t, b.Fragments["new-frag"].Content, "new-frag",
		"placeholder content names the new fragment")
	assert.Contains(t, buf.String(), "Added fragment: new-frag")
}

func TestApplyBundleEdits_AddFragmentDuplicateIsInformationalNoOp(t *testing.T) {
	b := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{
		"existing": {Content: "stable"},
	}}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		AddFragments: []string{"existing"},
	}, iox.NewErrWriter(&buf))

	assert.False(t, modified)
	assert.Equal(t, "stable", b.Fragments["existing"].Content,
		"duplicate add must not clobber existing content")
	assert.Contains(t, buf.String(), "Fragment already exists: existing")
}

func TestApplyBundleEdits_RemoveFragment(t *testing.T) {
	b := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{
		"target": {Content: "x"},
		"keep":   {Content: "y"},
	}}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		RemoveFragments: []string{"target", "missing"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified, "at least one removal landed")
	assert.NotContains(t, b.Fragments, "target")
	assert.Contains(t, b.Fragments, "keep")

	out := buf.String()
	assert.Contains(t, out, "Removed fragment: target")
	assert.Contains(t, out, "Fragment not found: missing")
}

func TestApplyBundleEdits_RemoveFragmentFromNilMapIsSilent(t *testing.T) {
	// Bundle has no fragments at all. RemoveFragments must not panic,
	// must not flip modified, and must not even log "not found" — the
	// helper short-circuits when b.Fragments is nil.
	b := &bundles.Bundle{}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		RemoveFragments: []string{"anything"},
	}, iox.NewErrWriter(&buf))

	assert.False(t, modified)
	assert.Empty(t, buf.String())
}

func TestApplyBundleEdits_AddPromptCreatesPlaceholder(t *testing.T) {
	b := &bundles.Bundle{}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		AddPrompts: []string{"new-prompt"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	require.Contains(t, b.Prompts, "new-prompt")
	assert.Equal(t, "new-prompt", b.Prompts["new-prompt"].Description,
		"prompt placeholder uses its name as the default description")
	assert.Equal(t, "Add prompt content here.", b.Prompts["new-prompt"].Content)
}

func TestApplyBundleEdits_AddMCPCreatesPlaceholderWithCommand(t *testing.T) {
	b := &bundles.Bundle{}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		AddMCP: []string{"mcp-fs"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	require.Contains(t, b.MCP, "mcp-fs")
	assert.Equal(t, "mcp-fs", b.MCP["mcp-fs"].Command,
		"MCP placeholder uses its name as the default command")
}

func TestApplyBundleEdits_AllSectionsTogether(t *testing.T) {
	// One invocation touches every slot. Confirms slots don't interfere.
	b := &bundles.Bundle{
		Tags:      []string{"keep-tag"},
		Fragments: map[string]bundles.BundleFragment{"keep-frag": {Content: "ok"}},
	}
	var buf bytes.Buffer
	modified := applyBundleEdits(b, bundleEdits{
		Description:     "new desc",
		Version:         "2.0",
		AddTags:         []string{"add-tag"},
		AddFragments:    []string{"new-frag"},
		RemoveFragments: []string{"keep-frag"},
		AddPrompts:      []string{"new-prompt"},
		AddMCP:          []string{"new-mcp"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	assert.Equal(t, "new desc", b.Description)
	assert.Equal(t, "2.0", b.Version)
	assert.Equal(t, []string{"keep-tag", "add-tag"}, b.Tags)
	assert.Contains(t, b.Fragments, "new-frag")
	assert.NotContains(t, b.Fragments, "keep-frag")
	assert.Contains(t, b.Prompts, "new-prompt")
	assert.Contains(t, b.MCP, "new-mcp")
}

// =============================================================================
// stdinConfirmer
// =============================================================================

func TestStdinConfirmer_AcceptsY(t *testing.T) {
	confirm := stdinConfirmer(strings.NewReader("y\n"))
	assert.True(t, confirm("prompt? "))
}

func TestStdinConfirmer_AcceptsCapitalY(t *testing.T) {
	confirm := stdinConfirmer(strings.NewReader("Y\n"))
	assert.True(t, confirm("prompt? "))
}

func TestStdinConfirmer_RejectsAnythingElse(t *testing.T) {
	for _, input := range []string{"n", "no", "yes", "yep", ""} {
		t.Run(input, func(t *testing.T) {
			confirm := stdinConfirmer(strings.NewReader(input + "\n"))
			assert.False(t, confirm("prompt? "),
				"only literal y/Y counts as confirmation; %q must be no", input)
		})
	}
}

func TestStdinConfirmer_EOFIsNo(t *testing.T) {
	// errReader returning io.EOF lands as Fscanln err != nil with
	// answer == "" → comparison correctly says no.
	confirm := stdinConfirmer(strings.NewReader(""))
	assert.False(t, confirm("prompt? "), "EOF without input must read as no, not a panic")
}

func TestStdinConfirmer_ReadErrorIsNo(t *testing.T) {
	// An io.Reader that errors on read also returns no (the variable
	// never gets populated). errReader is defined in session_cmd_test.go.
	confirm := stdinConfirmer(errReader{})
	assert.False(t, confirm("prompt? "))
}
