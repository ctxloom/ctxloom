// Tests for the pure helpers added to cmd/mcp_tools_review.go: the
// structural bundle diff used by show_bundle_verbatim's Diff field. The
// MCP handlers themselves are exercised via the wire-protocol
// integration tests in mcp_review_integration_test.go.
package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
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

// =============================================================================
// handleAcknowledgeBundleReview — disk reconciliation
// =============================================================================

func writePendingBundle(t *testing.T, baseDir string, entries map[string]remote.LockEntry) {
	t.Helper()
	mgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())
	require.NoError(t, mgr.Save(&remote.Lockfile{Bundles: entries, Profiles: map[string]remote.LockEntry{}}))
}

func TestHandleAcknowledgeBundleReview_ReconcilesFromDisk(t *testing.T) {
	// Regression: a CLI `remote sync` can write lock.pending.yaml while the
	// MCP server is already running, so the in-memory review tracker never
	// sees it. acknowledge_bundle_review must merge what's on disk rather
	// than reporting "no pending".
	dir := withProjectDir(t)
	baseDir := filepath.Join(dir, ".ctxloom")
	writePendingBundle(t, baseDir, map[string]remote.LockEntry{"r/a": {SHA: "111"}})

	s := &ctxServer{cfg: &config.Config{AppPaths: []string{baseDir}}, review: &bundleReviewState{}}
	require.False(t, s.review.hasPending(), "precondition: in-memory tracker empty")

	_, res, err := s.handleAcknowledgeBundleReview(context.Background(), nil, acknowledgeBundleReviewInput{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "approved", res.Status)
	assert.Equal(t, 1, res.Merged)

	active, err := remote.NewLockfileManager(baseDir).Load()
	require.NoError(t, err)
	assert.Contains(t, active.Bundles, "r/a", "pending entry merged into active")
}

func TestHandleAcknowledgeBundleReview_TrulyEmptyReportsNoPending(t *testing.T) {
	dir := withProjectDir(t)
	s := &ctxServer{cfg: &config.Config{AppPaths: []string{filepath.Join(dir, ".ctxloom")}}, review: &bundleReviewState{}}

	_, res, err := s.handleAcknowledgeBundleReview(context.Background(), nil, acknowledgeBundleReviewInput{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "no_pending", res.Status)
}

func TestHandleTrustRemote_PromotesPendingFromDisk(t *testing.T) {
	// Regression: trust_remote used to promote only what the in-memory
	// review tracker held. Bundles stranded in lock.pending.yaml on disk
	// (e.g. carried over from a previous session, or written by a CLI
	// `remote sync` while the server runs) were reported "auto-approved 0"
	// and left in pending — invisible to search_content, which only seeds
	// remote bundles from the active lockfile. Trust must promote them all.
	dir := withProjectDir(t)
	baseDir := filepath.Join(dir, ".ctxloom")
	cfg := &config.Config{AppPaths: []string{baseDir}}

	reg, err := operations.GetRegistry(cfg)
	require.NoError(t, err)
	require.NoError(t, reg.Add("r", "https://github.com/example/r"))

	writePendingBundle(t, baseDir, map[string]remote.LockEntry{
		"r/a":     {SHA: "111"},
		"r/b":     {SHA: "222"},
		"other/c": {SHA: "333"},
	})

	s := &ctxServer{cfg: cfg, review: &bundleReviewState{}}
	require.False(t, s.review.hasPending(), "precondition: in-memory tracker empty")

	_, res, err := s.handleTrustRemote(context.Background(), nil, trustRemoteInput{Name: "r", Trust: true})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Trust)
	assert.Equal(t, []string{"r/a", "r/b"}, res.Approved, "every pending entry from r promoted despite empty tracker")

	active, err := remote.NewLockfileManager(baseDir).Load()
	require.NoError(t, err)
	assert.Contains(t, active.Bundles, "r/a")
	assert.Contains(t, active.Bundles, "r/b")
	assert.NotContains(t, active.Bundles, "other/c", "untrusted remote stays pending")

	pending, err := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile()).Load()
	require.NoError(t, err)
	assert.NotContains(t, pending.Bundles, "r/a")
	assert.Contains(t, pending.Bundles, "other/c")
}

func TestHandleApproveRemotePending_PromotesPendingFromDisk(t *testing.T) {
	// Same root cause as trust_remote: approve_remote_pending must promote
	// straight off the on-disk pending lockfile rather than the in-memory
	// tracker, but without persisting trust.
	dir := withProjectDir(t)
	baseDir := filepath.Join(dir, ".ctxloom")
	cfg := &config.Config{AppPaths: []string{baseDir}}

	writePendingBundle(t, baseDir, map[string]remote.LockEntry{
		"r/a":     {SHA: "111"},
		"other/c": {SHA: "333"},
	})

	s := &ctxServer{cfg: cfg, review: &bundleReviewState{}}
	require.False(t, s.review.hasPending(), "precondition: in-memory tracker empty")

	_, res, err := s.handleApproveRemotePending(context.Background(), nil, approveRemotePendingInput{Remote: "r"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []string{"r/a"}, res.Approved)

	active, err := remote.NewLockfileManager(baseDir).Load()
	require.NoError(t, err)
	assert.Contains(t, active.Bundles, "r/a")
	assert.NotContains(t, active.Bundles, "other/c")
}
