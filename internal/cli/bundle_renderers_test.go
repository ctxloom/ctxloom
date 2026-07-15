// Tests for the list/show renderers extracted from runBundleList and
// runBundleShow. The cobra wrappers are now trivial composition (load
// config + loader + delegate); the testable surface is the formatting
// decisions (singular/plural MCP, distilled-vs-no_distill markers,
// suppressed-when-empty sections, 70-char first-line truncation).
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// =============================================================================
// renderBundleList
// =============================================================================

func TestRenderBundleList_EmptyShowsInstallHint(t *testing.T) {
	var buf bytes.Buffer
	assert.NoError(t, renderBundleList(&buf, nil))
	out := buf.String()
	assert.Contains(t, out, "No bundles installed.")
	// The hint must use the reference-only vocabulary: reference from a
	// profile, then pull. There is no `ctxloom install`.
	assert.Contains(t, out, "ctxloom remote pull")
}

func TestRenderBundleList_SingleBundleAllFields(t *testing.T) {
	infos := []*bundles.BundleInfo{{
		Name:          "alice/coding",
		Version:       "1.2.3",
		Description:   "Coding standards",
		Tags:          []string{"go", "best-practices"},
		FragmentCount: 3,
		CommandCount:  2,
		MCPCount:      1,
	}}

	var buf bytes.Buffer
	assert.NoError(t, renderBundleList(&buf, infos))
	out := buf.String()

	assert.Contains(t, out, "Installed bundles (1):")
	assert.Contains(t, out, "  alice/coding (v1.2.3)")
	assert.Contains(t, out, "    Coding standards")
	assert.Contains(t, out, "Contains: 3 fragments, 2 commands, 1 MCP server",
		"singular MCP server uses the 1-server form")
	assert.Contains(t, out, "Tags: go, best-practices")
}

func TestRenderBundleList_PluralMCPServers(t *testing.T) {
	// Pin the singular/plural distinction — the only place in the bundle
	// list where MCP count branches on value. Off-by-one would silently
	// produce "2 MCP server".
	infos := []*bundles.BundleInfo{{Name: "x", MCPCount: 2}}
	var buf bytes.Buffer
	assert.NoError(t, renderBundleList(&buf, infos))
	assert.Contains(t, buf.String(), "Contains: 2 MCP servers")
	assert.NotContains(t, buf.String(), "2 MCP server\n", "must not produce singular form for >1")
}

func TestRenderBundleList_OmitsOptionalSections(t *testing.T) {
	// A bundle with only a Name should produce just the name line and a
	// trailing blank — no Description, no Contains, no Tags.
	infos := []*bundles.BundleInfo{{Name: "minimal"}}
	var buf bytes.Buffer
	assert.NoError(t, renderBundleList(&buf, infos))
	out := buf.String()

	assert.Contains(t, out, "  minimal\n")
	assert.NotContains(t, out, "Contains:")
	assert.NotContains(t, out, "Tags:")
	assert.NotContains(t, out, "(v")
}

func TestRenderBundleList_ZeroCountsSuppressContainsLine(t *testing.T) {
	// A bundle with all three counts at zero should not emit a Contains
	// line at all (rather than emitting "Contains: " with no parts).
	infos := []*bundles.BundleInfo{{Name: "empty", FragmentCount: 0, CommandCount: 0, MCPCount: 0}}
	var buf bytes.Buffer
	assert.NoError(t, renderBundleList(&buf, infos))
	assert.NotContains(t, buf.String(), "Contains:")
}

// =============================================================================
// renderBundleShow
// =============================================================================

func TestRenderBundleShow_FullBundle(t *testing.T) {
	b := &bundles.Bundle{
		Name:        "developer",
		Path:        "/proj/.ctxloom/bundles/developer.yaml",
		Version:     "1.0.0",
		Author:      "alice",
		Description: "Dev context",
		Tags:        []string{"go", "review"},
		Notes:       "Internal usage only.",
		Fragments: map[string]bundles.BundleFragment{
			"styles": {
				Tags:      []string{"docs"},
				Distilled: "compressed",
				Content:   "First line\nrest of the content",
			},
		},
		Commands: map[string]bundles.BundleCommand{
			"review": {
				Tags:        []string{"review"},
				Description: "Run a code review",
				NoDistill:   true,
				Content:     "Body irrelevant for show",
			},
		},
		MCP: map[string]bundles.BundleMCP{
			"fs": {
				Command:      "mcp-fs",
				Args:         []string{"--root", "/tmp"},
				Env:          map[string]string{"DEBUG": "1"},
				Notes:        "Filesystem access",
				Installation: "go install ...",
			},
		},
	}

	var buf bytes.Buffer
	assert.NoError(t, renderBundleShow(&buf, b))
	out := buf.String()

	// Header lines.
	assert.Contains(t, out, "Bundle: developer")
	assert.Contains(t, out, "Version: 1.0.0")
	assert.Contains(t, out, "Author: alice")
	assert.Contains(t, out, "Description: Dev context")
	assert.Contains(t, out, "Tags: go, review")
	assert.Contains(t, out, "Path: /proj/.ctxloom/bundles/developer.yaml")

	// MCP section.
	assert.Contains(t, out, "MCP Servers (1):")
	assert.Contains(t, out, "  - fs")
	assert.Contains(t, out, "Command: mcp-fs")
	assert.Contains(t, out, "Args: --root /tmp")
	assert.Contains(t, out, "Env:")
	assert.Contains(t, out, "DEBUG=1")
	assert.Contains(t, out, "Notes: Filesystem access")
	assert.Contains(t, out, "Installation: go install ...")

	// Fragment entry: tag list, distilled marker, first-line preview.
	assert.Contains(t, out, "Fragments (1):")
	assert.Contains(t, out, "  - styles [docs] (distilled)")
	assert.Contains(t, out, "First line")

	// Prompt entry: tag list, no_distill marker (precedence: Distilled
	// is empty, so NoDistill wins), description line.
	assert.Contains(t, out, "Commands (1):")
	assert.Contains(t, out, "  - review [review] (no_distill)")
	assert.Contains(t, out, "Run a code review")

	// Notes block.
	assert.Contains(t, out, "Notes:\n  Internal usage only.")
}

func TestRenderBundleShow_OmitsEmptySections(t *testing.T) {
	b := &bundles.Bundle{Name: "minimal", Path: "/p"}
	var buf bytes.Buffer
	assert.NoError(t, renderBundleShow(&buf, b))
	out := buf.String()

	for _, banned := range []string{"Version:", "Author:", "Description:", "Tags:",
		"MCP Servers", "Fragments", "Commands", "Notes:"} {
		assert.NotContains(t, out, banned, "empty bundle must not emit %q", banned)
	}
	assert.Contains(t, out, "Bundle: minimal")
	assert.Contains(t, out, "Path: /p")
}

// =============================================================================
// renderBundleFragmentEntry — distilled/no_distill precedence + truncation
// =============================================================================

func TestRenderBundleFragmentEntry_DistilledWinsOverNoDistill(t *testing.T) {
	// If both Distilled is non-empty AND NoDistill is true, the switch
	// orders Distilled first. Pin that — otherwise it would be ambiguous
	// which marker the user sees.
	frag := bundles.BundleFragment{
		Distilled: "X",
		NoDistill: true,
		Content:   "first",
	}
	var buf bytes.Buffer
	renderBundleFragmentEntry(iox.NewErrWriter(&buf), "f", frag)
	out := buf.String()
	assert.Contains(t, out, "(distilled)")
	assert.NotContains(t, out, "(no_distill)")
}

func TestRenderBundleFragmentEntry_NoMarkerWhenPlain(t *testing.T) {
	frag := bundles.BundleFragment{Content: "first"}
	var buf bytes.Buffer
	renderBundleFragmentEntry(iox.NewErrWriter(&buf), "f", frag)
	out := buf.String()
	assert.NotContains(t, out, "(distilled)")
	assert.NotContains(t, out, "(no_distill)")
}

func TestRenderBundleFragmentEntry_TruncatesLongFirstLine(t *testing.T) {
	// 70-char preview cap: 67 chars + "..." = 70 total.
	long := strings.Repeat("x", 200)
	frag := bundles.BundleFragment{Content: long}
	var buf bytes.Buffer
	renderBundleFragmentEntry(iox.NewErrWriter(&buf), "verbose", frag)
	out := buf.String()
	assert.Contains(t, out, strings.Repeat("x", 67)+"...")
	assert.NotContains(t, out, strings.Repeat("x", 68), "preview must not exceed 67 chars + ellipsis")
}

func TestRenderBundleFragmentEntry_NoTagBracketsWhenEmpty(t *testing.T) {
	// Empty tag list must not produce empty brackets `[]`.
	frag := bundles.BundleFragment{Content: "x"}
	var buf bytes.Buffer
	renderBundleFragmentEntry(iox.NewErrWriter(&buf), "f", frag)
	assert.NotContains(t, buf.String(), "[]")
}

// =============================================================================
// renderBundleCommandEntry — description optional, marker precedence
// =============================================================================

func TestRenderBundleCommandEntry_DistilledMarker(t *testing.T) {
	prompt := bundles.BundleCommand{Distilled: "X", Description: "desc"}
	var buf bytes.Buffer
	renderBundleCommandEntry(iox.NewErrWriter(&buf), "p", prompt)
	assert.Contains(t, buf.String(), "(distilled)")
}

func TestRenderBundleCommandEntry_NoDistillMarker(t *testing.T) {
	prompt := bundles.BundleCommand{NoDistill: true}
	var buf bytes.Buffer
	renderBundleCommandEntry(iox.NewErrWriter(&buf), "p", prompt)
	assert.Contains(t, buf.String(), "(no_distill)")
}

func TestRenderBundleCommandEntry_DescriptionOptional(t *testing.T) {
	// No description means no second line. Just the name line.
	prompt := bundles.BundleCommand{}
	var buf bytes.Buffer
	renderBundleCommandEntry(iox.NewErrWriter(&buf), "p", prompt)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.Len(t, lines, 1, "no description → single line (name only)")
}

// =============================================================================
// renderBundleMCPEntry — optional fields suppressed when empty
// =============================================================================

func TestRenderBundleMCPEntry_MinimalShowsCommandOnly(t *testing.T) {
	mcp := bundles.BundleMCP{Command: "mcp-fs"}
	var buf bytes.Buffer
	renderBundleMCPEntry(iox.NewErrWriter(&buf), "fs", mcp)
	out := buf.String()
	assert.Contains(t, out, "  - fs\n")
	assert.Contains(t, out, "Command: mcp-fs")
	assert.NotContains(t, out, "Args:")
	assert.NotContains(t, out, "Env:")
	assert.NotContains(t, out, "Notes:")
	assert.NotContains(t, out, "Installation:")
}

// =============================================================================
// parseBundleViewRef
// =============================================================================

func TestParseBundleViewRef(t *testing.T) {
	cases := []struct {
		in         string
		wantBundle string
		wantPath   string
	}{
		{"plain", "plain", ""},
		{"name#fragments/intro", "name", "fragments/intro"},
		{"name#mcp/default", "name", "mcp/default"},
		{"name#", "name", ""}, // trailing #, empty path
		{"#foo", "", "foo"},   // leading # is unusual but well-defined
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotBundle, gotPath := parseBundleViewRef(tc.in)
			assert.Equal(t, tc.wantBundle, gotBundle)
			assert.Equal(t, tc.wantPath, gotPath)
		})
	}
}

// =============================================================================
// renderBundleViewItem
// =============================================================================

func viewBundle() *bundles.Bundle {
	return &bundles.Bundle{
		Fragments: map[string]bundles.BundleFragment{
			"intro":    {Content: "hello world"},
			"distill":  {Content: "raw body", Distilled: "compressed body"},
			"trailing": {Content: "already ends\n"},
		},
		Commands: map[string]bundles.BundleCommand{
			"review":  {Content: "do a review"},
			"distill": {Content: "raw prompt", Distilled: "compressed prompt"},
		},
		MCP: map[string]bundles.BundleMCP{
			"fs": {Command: "mcp-fs"},
		},
	}
}

func TestRenderBundleViewItem_BadPathFormat(t *testing.T) {
	err := renderBundleViewItem(&bytes.Buffer{}, viewBundle(), "fragments-only", false)
	assert.ErrorContains(t, err, "invalid path format")
}

func TestRenderBundleViewItem_UnknownItemType(t *testing.T) {
	err := renderBundleViewItem(&bytes.Buffer{}, viewBundle(), "weird/intro", false)
	assert.ErrorContains(t, err, "unknown item type")
}

func TestRenderBundleViewItem_Fragment_RawAndDistilled(t *testing.T) {
	t.Run("raw_appended_newline", func(t *testing.T) {
		var buf bytes.Buffer
		require := assert.New(t)
		require.NoError(renderBundleViewItem(&buf, viewBundle(), "fragments/intro", false))
		require.Equal("hello world\n", buf.String(), "non-newline-terminated content gets a trailing newline")
	})

	t.Run("raw_existing_newline_preserved", func(t *testing.T) {
		var buf bytes.Buffer
		require := assert.New(t)
		require.NoError(renderBundleViewItem(&buf, viewBundle(), "fragments/trailing", false))
		require.Equal("already ends\n", buf.String(), "no double newline when content already terminated")
	})

	t.Run("distilled_picked_when_flag_set", func(t *testing.T) {
		var buf bytes.Buffer
		require := assert.New(t)
		require.NoError(renderBundleViewItem(&buf, viewBundle(), "fragments/distill", true))
		got := buf.String()
		require.Contains(got, "# (distilled version)")
		require.Contains(got, "compressed body")
		require.NotContains(got, "raw body")
	})

	t.Run("distilled_falls_back_when_empty", func(t *testing.T) {
		// Asking for distilled on a fragment with no Distilled value
		// falls back to raw silently — the marker header is suppressed.
		var buf bytes.Buffer
		require := assert.New(t)
		require.NoError(renderBundleViewItem(&buf, viewBundle(), "fragments/intro", true))
		got := buf.String()
		require.Equal("hello world\n", got)
		require.NotContains(got, "distilled")
	})
}

func TestRenderBundleViewItem_FragmentNotFound(t *testing.T) {
	err := renderBundleViewItem(&bytes.Buffer{}, viewBundle(), "fragments/missing", false)
	assert.ErrorContains(t, err, "fragment not found")
}

func TestRenderBundleViewItem_Prompt_Distilled(t *testing.T) {
	var buf bytes.Buffer
	require := assert.New(t)
	require.NoError(renderBundleViewItem(&buf, viewBundle(), "commands/distill", true))
	got := buf.String()
	require.Contains(got, "compressed prompt")
	require.Contains(got, "# (distilled version)")
}

func TestRenderBundleViewItem_PromptNotFound(t *testing.T) {
	err := renderBundleViewItem(&bytes.Buffer{}, viewBundle(), "commands/missing", false)
	assert.ErrorContains(t, err, "command not found")
}

func TestRenderBundleViewItem_MCP_YAMLOutput(t *testing.T) {
	var buf bytes.Buffer
	require := assert.New(t)
	require.NoError(renderBundleViewItem(&buf, viewBundle(), "mcp/fs", false))
	got := buf.String()
	require.Contains(got, "# MCP Server: fs")
	require.Contains(got, "command: mcp-fs", "yaml key is lowercase")
}

// =============================================================================
// lookupBundleMCP — single-server fallback
// =============================================================================

func TestLookupBundleMCP_ExactMatch(t *testing.T) {
	b := viewBundle()
	got, name, ok := lookupBundleMCP(b, "fs")
	assert.True(t, ok)
	assert.Equal(t, "fs", name)
	assert.Equal(t, "mcp-fs", got.Command)
}

func TestLookupBundleMCP_SingleServerFallback(t *testing.T) {
	// Bundle with one MCP under an unrelated key — looking up "anything"
	// should still return that single server (with its real key).
	b := &bundles.Bundle{MCP: map[string]bundles.BundleMCP{
		"only-one": {Command: "x"},
	}}
	got, name, ok := lookupBundleMCP(b, "default")
	assert.True(t, ok, "single-server bundle returns its lone server regardless of asked-for name")
	assert.Equal(t, "only-one", name)
	assert.Equal(t, "x", got.Command)
}

func TestLookupBundleMCP_MultiServerNoFallback(t *testing.T) {
	// With more than one server, the fallback must NOT trigger — an
	// unknown name is unambiguously "not found".
	b := &bundles.Bundle{MCP: map[string]bundles.BundleMCP{
		"a": {Command: "ca"},
		"b": {Command: "cb"},
	}}
	_, _, ok := lookupBundleMCP(b, "default")
	assert.False(t, ok)
}

func TestLookupBundleMCP_EmptyMap(t *testing.T) {
	b := &bundles.Bundle{}
	_, _, ok := lookupBundleMCP(b, "anything")
	assert.False(t, ok)
}
