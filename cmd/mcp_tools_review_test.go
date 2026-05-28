// Tests for the pure helpers added to cmd/mcp_tools_review.go: the
// structural bundle diff used by show_bundle_verbatim's Diff field. The
// MCP handlers themselves are exercised via the wire-protocol
// integration tests in mcp_review_integration_test.go.
package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// diffBundleYAMLs
// =============================================================================

const oldBundle = `version: "1.0.0"
fragments:
  keep:
    content: "stable"
  modify-me:
    content: "old text"
  remove-me:
    content: "going away"
prompts:
  prompt-a:
    content: "alpha"
mcp:
  fs:
    command: "old-mcp"
hooks:
  PostTool:
    - command: "hook-1"
    - command: "hook-2"
`

const newBundle = `version: "1.0.0"
fragments:
  keep:
    content: "stable"
  modify-me:
    content: "NEW text"
  add-me:
    content: "fresh"
prompts:
  prompt-a:
    content: "alpha"
mcp:
  fs:
    command: "new-mcp"
hooks:
  PostTool:
    - command: "hook-1"
    - command: "hook-2"
    - command: "hook-3"
`

func TestDiffBundleYAMLs_FullStructuralDelta(t *testing.T) {
	got := diffBundleYAMLs([]byte(oldBundle), []byte(newBundle))

	// Fragments: + add-me, - remove-me, ~ modify-me. Stable "keep" must
	// NOT surface.
	assert.Contains(t, got, "Fragments:")
	assert.Contains(t, got, "  + add-me")
	assert.Contains(t, got, "  - remove-me")
	assert.Contains(t, got, "  ~ modify-me")
	assert.NotContains(t, got, "keep", "unchanged fragment must not appear")

	// Prompts: no changes — section header suppressed.
	assert.NotContains(t, got, "Prompts:")

	// MCP servers: fs's command changed.
	assert.Contains(t, got, "MCP servers:")
	assert.Contains(t, got, "  ~ fs")

	// Hooks: PostTool count went 2 → 3.
	assert.Contains(t, got, "Hooks:")
	assert.Contains(t, got, "PostTool: 2 → 3")
}

func TestDiffBundleYAMLs_NoChanges(t *testing.T) {
	// Identical YAML → message that says SHA changed but content didn't.
	const same = `version: "1.0.0"
fragments:
  a:
    content: "x"
`
	got := diffBundleYAMLs([]byte(same), []byte(same))
	assert.Contains(t, got, "no structural changes")
}

func TestDiffBundleYAMLs_OnlyAdds(t *testing.T) {
	const before = `version: "1.0.0"
fragments:
  keep:
    content: "x"
`
	const after = `version: "1.0.0"
fragments:
  keep:
    content: "x"
  new1:
    content: "y"
  new2:
    content: "z"
`
	got := diffBundleYAMLs([]byte(before), []byte(after))
	assert.Contains(t, got, "  + new1")
	assert.Contains(t, got, "  + new2")
	assert.NotContains(t, got, "  -", "no removals expected")
	assert.NotContains(t, got, "  ~", "no modifications expected")
}

func TestDiffBundleYAMLs_OnlyRemovals(t *testing.T) {
	const before = `version: "1.0.0"
fragments:
  keep:
    content: "x"
  dropme:
    content: "y"
`
	const after = `version: "1.0.0"
fragments:
  keep:
    content: "x"
`
	got := diffBundleYAMLs([]byte(before), []byte(after))
	assert.Contains(t, got, "  - dropme")
	assert.NotContains(t, got, "  +")
	assert.NotContains(t, got, "  ~")
}

func TestDiffBundleYAMLs_ParseError(t *testing.T) {
	// Malformed YAML on either side surfaces as a clear marker. The
	// handler's fallback is to return Content without Diff.
	got := diffBundleYAMLs([]byte("not: [valid: yaml"), []byte(newBundle))
	assert.Contains(t, got, "parse error on old bundle")

	got = diffBundleYAMLs([]byte(oldBundle), []byte("not: [valid: yaml"))
	assert.Contains(t, got, "parse error on new bundle")
}

func TestDiffBundleYAMLs_HookCountReportedPerEvent(t *testing.T) {
	// Multiple events, each with independent count deltas.
	const before = `version: "1.0.0"
hooks:
  PostTool:
    - command: "a"
  SessionStart:
    - command: "x"
    - command: "y"
`
	const after = `version: "1.0.0"
hooks:
  PostTool:
    - command: "a"
    - command: "b"
  SessionStart:
    - command: "x"
`
	got := diffBundleYAMLs([]byte(before), []byte(after))
	assert.Contains(t, got, "PostTool: 1 → 2")
	assert.Contains(t, got, "SessionStart: 2 → 1")
}

func TestDiffBundleYAMLs_NewHookEventCountsAsChange(t *testing.T) {
	// Adding a brand-new event type (e.g. introducing PreTool hooks) must
	// show up — len(oldHooks[event]) = 0 vs len(newHooks[event]) = N.
	const before = `version: "1.0.0"
`
	const after = `version: "1.0.0"
hooks:
  PreTool:
    - command: "new"
`
	got := diffBundleYAMLs([]byte(before), []byte(after))
	assert.Contains(t, got, "PreTool: 0 → 1")
}
