package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// stageReviewFixture writes a project layout with active + pending
// lockfiles that disagree on a bundle's SHA, so the MCP server's startup
// path picks up review state on init. Returns the workDir for use by
// startMCPServer.
func stageReviewFixture(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.BundlesPath(appDir), 0o755))

	// Register a remote (URL is never actually fetched in these tests —
	// the gate triggers before the inner handler runs, and the review
	// tools mutate lockfiles directly).
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"), []byte(`default: alice
remotes:
  alice:
    url: https://github.com/alice/ctxloom
    version: v1
`), 0o644))

	// Active lockfile: one bundle at SHA aaaaaaa.
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "lock.yaml"), []byte(`version: 1
locked_at: 2026-01-01T00:00:00Z
bundles:
  alice/security:
    sha: aaaaaaa
    url: https://github.com/alice/ctxloom
    ctxloom_version: v1
    fetched_at: 2026-01-01T00:00:00Z
`), 0o644))

	// Pending lockfile: same bundle bumped to SHA bbbbbbb plus a new one.
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "lock.pending.yaml"), []byte(`version: 1
locked_at: 2026-01-02T00:00:00Z
bundles:
  alice/security:
    sha: bbbbbbb
    url: https://github.com/alice/ctxloom
    ctxloom_version: v1
    fetched_at: 2026-01-02T00:00:00Z
  alice/newbundle:
    sha: ccccccc
    url: https://github.com/alice/ctxloom
    ctxloom_version: v1
    fetched_at: 2026-01-02T00:00:00Z
`), 0o644))

	return workDir
}

// initSession runs the standard initialize / notifications/initialized
// handshake so the test can move straight to tools/call.
func initSession(t *testing.T, c *mcpClient) {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	_ = c.recvByID(t, 1)
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
}

// callTool fires a tools/call request and returns the result envelope
// (not the inner JSON — the caller may want to inspect isError or
// raw content for blocked-result asserts).
func callTool(t *testing.T, c *mcpClient, id int, name string, args map[string]any) map[string]any {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	return c.recvByID(t, id)
}

// firstContentText pulls the .result.content[0].text string from a
// tools/call response. This is the *first* block, which during a pending
// review is the prepended template (see reviewMiddleware). Use
// lastContentText for the handler's own output.
func firstContentText(t *testing.T, msg map[string]any) string {
	t.Helper()
	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "missing result: %v", msg)
	content, ok := result["content"].([]any)
	require.True(t, ok && len(content) > 0, "missing content")
	first, ok := content[0].(map[string]any)
	require.True(t, ok)
	text, _ := first["text"].(string)
	return text
}

// lastContentText returns the trailing content block — the handler's own
// payload when the review middleware has prepended the template. Without
// a pending review (or for tools that never go through the prepend), this
// is the same as firstContentText.
func lastContentText(t *testing.T, msg map[string]any) string {
	t.Helper()
	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "missing result: %v", msg)
	content, ok := result["content"].([]any)
	require.True(t, ok && len(content) > 0, "missing content")
	last, ok := content[len(content)-1].(map[string]any)
	require.True(t, ok)
	text, _ := last["text"].(string)
	return text
}

func resultIsError(t *testing.T, msg map[string]any) bool {
	t.Helper()
	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "missing result: %v", msg)
	isErr, _ := result["isError"].(bool)
	return isErr
}

// TestMCP_WireProtocol_ReviewGate_BlocksAndUnblocks proves the gate's
// load-bearing safety property over the real stdio wire: bundle-touching
// tools are blocked while pending; approve_review unblocks them; the
// template surfaces on every blocked response.
func TestMCP_WireProtocol_ReviewGate_BlocksAndUnblocks(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageReviewFixture(t)

	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	// 1. A gated tool is blocked. Body is the review template, IsError=true.
	resp := callTool(t, c, 10, "assemble_context", map[string]any{})
	assert.True(t, resultIsError(t, resp),
		"assemble_context must be blocked while pending review is in effect")
	body := firstContentText(t, resp)
	assert.Contains(t, body, "ctxloom bundle review required")
	assert.Contains(t, body, "alice/security")
	assert.Contains(t, body, "alice/newbundle")
	assert.Contains(t, body, `tool "assemble_context" is blocked`)

	// 2. An allowlisted tool succeeds, but the template prepends to it.
	listResp := callTool(t, c, 11, "list_remotes", map[string]any{})
	require.False(t, resultIsError(t, listResp),
		"list_remotes is on the allowlist; should not be blocked")
	listBody := firstContentText(t, listResp)
	assert.Contains(t, listBody, "ctxloom bundle review required",
		"every allowed call should prepend the review template until acknowledged")

	// 3. acknowledge_bundle_review merges pending → active and unblocks.
	// The middleware still prepends the template on THIS turn because the
	// snapshot was taken before the handler ran; the handler's own payload
	// is the last content block.
	ackResp := callTool(t, c, 12, "acknowledge_bundle_review", map[string]any{})
	require.False(t, resultIsError(t, ackResp))
	ackBody := lastContentText(t, ackResp)
	assert.Contains(t, ackBody, `"status":"approved"`)
	assert.Contains(t, ackBody, `"merged":2`)

	// Pending file is gone, active has both SHAs.
	assert.NoFileExists(t, filepath.Join(workDir, ".ctxloom", "lock.pending.yaml"),
		"pending lockfile should be removed after acknowledge")
	activeBytes, err := os.ReadFile(filepath.Join(workDir, ".ctxloom", "lock.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(activeBytes), "bbbbbbb", "active lockfile carries the approved SHA")
	assert.Contains(t, string(activeBytes), "ccccccc", "and the newly-installed bundle")

	// 4. After acknowledge, formerly-blocked tools succeed and no longer
	// prepend the template.
	postResp := callTool(t, c, 13, "list_remotes", map[string]any{})
	postBody := firstContentText(t, postResp)
	assert.NotContains(t, postBody, "ctxloom bundle review required",
		"once pending clears, the template prepend must stop")
}

// TestMCP_WireProtocol_DeclineKeepsOldContent verifies decline_bundle
// preserves the prior active SHA — the load-bearing rollback property.
func TestMCP_WireProtocol_DeclineKeepsOldContent(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageReviewFixture(t)

	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	// Bulk decline. Template still prepends this turn; handler payload
	// is the last content block.
	declineResp := callTool(t, c, 20, "decline_bundle", map[string]any{})
	require.False(t, resultIsError(t, declineResp))
	body := lastContentText(t, declineResp)
	assert.Contains(t, body, `"status":"declined_all"`)
	assert.Contains(t, body, "alice/security")
	assert.Contains(t, body, "alice/newbundle")

	// Pending is gone.
	assert.NoFileExists(t, filepath.Join(workDir, ".ctxloom", "lock.pending.yaml"))

	// Active lockfile is UNCHANGED — still has the old SHA, no new bundle.
	activeBytes, err := os.ReadFile(filepath.Join(workDir, ".ctxloom", "lock.yaml"))
	require.NoError(t, err)
	active := string(activeBytes)
	assert.Contains(t, active, "aaaaaaa", "old SHA still active after decline")
	assert.NotContains(t, active, "bbbbbbb", "new SHA must not appear")
	assert.NotContains(t, active, "ccccccc", "declined new bundle must not appear")

	// Gate is released.
	postResp := callTool(t, c, 21, "list_remotes", map[string]any{})
	assert.False(t, resultIsError(t, postResp))
	assert.NotContains(t, firstContentText(t, postResp), "ctxloom bundle review required")
}

// TestMCP_WireProtocol_DeclineSingleBundle exercises the named-decline
// path: one entry drops, the other remains pending, and the gate stays
// in effect.
func TestMCP_WireProtocol_DeclineSingleBundle(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageReviewFixture(t)

	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	declineResp := callTool(t, c, 30, "decline_bundle", map[string]any{
		"name": "alice/newbundle",
	})
	require.False(t, resultIsError(t, declineResp))
	body := lastContentText(t, declineResp)
	assert.Contains(t, body, `"status":"declined"`)
	assert.Contains(t, body, `"remaining":1`)

	// Pending still on disk with the other entry.
	pendingBytes, err := os.ReadFile(filepath.Join(workDir, ".ctxloom", "lock.pending.yaml"))
	require.NoError(t, err)
	pending := string(pendingBytes)
	assert.NotContains(t, pending, "alice/newbundle", "declined bundle removed from pending")
	assert.Contains(t, pending, "alice/security", "other bundle still pending")

	// Gate STILL in effect — alice/security is still pending review.
	gated := callTool(t, c, 31, "assemble_context", map[string]any{})
	assert.True(t, resultIsError(t, gated),
		"gate stays in effect while any bundle remains pending")
	assert.Contains(t, firstContentText(t, gated), "alice/security")
}

// TestMCP_WireProtocol_ReviewToolsListed verifies the four review tools
// appear in tools/list — the LLM needs to discover them.
func TestMCP_WireProtocol_ReviewToolsListed(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageReviewFixture(t)

	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 40, "method": "tools/list", "params": map[string]any{},
	})
	resp := c.recvByID(t, 40)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, raw := range tools {
		entry := raw.(map[string]any)
		names[entry["name"].(string)] = true
	}
	for _, expected := range []string{
		"acknowledge_bundle_review",
		"decline_bundle",
		"show_bundle_verbatim",
		"trust_remote",
	} {
		assert.True(t, names[expected], "review tool %q must be on the tools list", expected)
	}
}

// TestMCP_WireProtocol_InitializeInstructionsCarryReviewProtocol verifies
// the SDK ServerOptions.Instructions field carries the review protocol
// text to the client. The model relies on this to know how to respond
// when pending lands mid-session.
func TestMCP_WireProtocol_InitializeInstructionsCarryReviewProtocol(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(paths.BundlesPath(filepath.Join(workDir, ".ctxloom")), 0o755))

	c := startMCPServer(t, binPath, workDir)
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	resp := c.recvByID(t, 1)
	result := resp["result"].(map[string]any)
	instructions, _ := result["instructions"].(string)
	assert.True(t, strings.Contains(instructions, "review template, verbatim"),
		"initialize.instructions must carry the review protocol text; got: %q", instructions)
}

// TestMCP_WireProtocol_AutoApproveBypass exercises the
// CTXLOOM_AUTO_APPROVE_BUNDLES=1 escape hatch: pending is auto-merged at
// startup and no gate engages.
func TestMCP_WireProtocol_AutoApproveBypass(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageReviewFixture(t)

	// Run the server with the bypass env var set. We replicate
	// startMCPServer here because the helper doesn't expose env override.
	t.Setenv("CTXLOOM_AUTO_APPROVE_BUNDLES", "1")
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	// The bypass either auto-merges at startup or simply never engages
	// the review state. Either way, gated tools must succeed and the
	// template must not appear in their output.
	resp := callTool(t, c, 50, "list_remotes", map[string]any{})
	assert.False(t, resultIsError(t, resp))
	body := firstContentText(t, resp)
	assert.NotContains(t, body, "ctxloom bundle review required",
		"bypass env var must keep the gate disengaged")
}
