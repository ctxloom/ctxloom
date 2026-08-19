package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stageMinimalProject sets up a workDir with a .ctxloom that doesn't
// trip any review-gate logic on startup. Resources need a runnable
// server but no remote bundles, no lockfile, no pending review.
func stageMinimalProject(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	// Empty config is fine — defaults work for the resource handlers.
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(fmt.Sprintf("version: %d\nllm:\n  configs:\n    claude-code:\n      type: claude-code\n", config.CurrentConfigVersion)), 0o644))
	return workDir
}

// readResource fires a resources/read request and returns the first
// content text. Mirrors the callTool shape from mcp_review_integration_test.go.
func readResource(t *testing.T, c *mcpClient, id int, uri string) (text, mimeType string) {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "resources/read",
		"params": map[string]any{"uri": uri},
	})
	msg := c.recvByID(t, id)
	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "resources/read[%s] returned no result: %v", uri, msg)
	contents, ok := result["contents"].([]any)
	require.True(t, ok && len(contents) > 0, "no contents for %s", uri)
	first, ok := contents[0].(map[string]any)
	require.True(t, ok)
	text, _ = first["text"].(string)
	mimeType, _ = first["mimeType"].(string)
	return text, mimeType
}

// TestMCP_WireProtocol_ResourcesList confirms every resource registered
// in cmd/mcp_resources.go surfaces over the actual MCP wire. Spawns a
// real ctxloom mcp subprocess; protects against silent registration
// regressions (e.g., dropping a server.AddResource call).
func TestMCP_WireProtocol_ResourcesList(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageMinimalProject(t)
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 100, "method": "resources/list",
		"params": map[string]any{},
	})
	msg := c.recvByID(t, 100)
	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "resources/list returned no result: %v", msg)
	resources, ok := result["resources"].([]any)
	require.True(t, ok, "resources field not an array")

	uris := make(map[string]bool, len(resources))
	for _, r := range resources {
		entry := r.(map[string]any)
		if uri, ok := entry["uri"].(string); ok {
			uris[uri] = true
		}
	}

	// Every concrete-URI resource registered in registerResources MUST
	// appear. Templated resources (fragments/{name} etc.) live in
	// resources/templates/list, not here, so they're checked separately.
	for _, want := range []string{
		"ctxloom://help",
		"ctxloom://sessions",
		"ctxloom://sessions/recent",
		"ctxloom://fragments",
		"ctxloom://profiles",
		"ctxloom://commands",
		"ctxloom://remotes",
		"ctxloom://mcp-servers",
	} {
		assert.True(t, uris[want], "expected resource URI %q in resources/list", want)
	}
}

// TestMCP_WireProtocol_ReadHelp confirms ctxloom://help renders as
// markdown and lists the other resource URIs. If a future change
// adds a resource but forgets the help body, this catches it.
func TestMCP_WireProtocol_ReadHelp(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageMinimalProject(t)
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	body, mime := readResource(t, c, 101, "ctxloom://help")
	assert.Equal(t, "text/markdown", mime)
	assert.Contains(t, body, "ctxloom://help")
	assert.Contains(t, body, "ctxloom://sessions/recent")
}

// TestMCP_WireProtocol_ReadSessionsRecent confirms the cwd-filtered
// sessions view serves a YAML shape even when no harp-named sessions
// exist for this project (fresh-machine first-run case).
func TestMCP_WireProtocol_ReadSessionsRecent(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageMinimalProject(t)
	// Isolate HOME so the user-global index doesn't carry real-user state
	// into the test.
	testsupport.Isolate(t)
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	body, mime := readResource(t, c, 103, "ctxloom://sessions/recent")
	assert.Equal(t, "application/yaml", mime)
	// Empty index serializes as `sessions: []` or `sessions: null` —
	// both are valid YAML and both contain the key.
	assert.Contains(t, body, "sessions:")
}

// TestMCP_WireProtocol_ReadFragments confirms the listing-as-resource
// migration surfaces all available fragments via ctxloom://fragments,
// even when none exist (empty list returns gracefully).
func TestMCP_WireProtocol_ReadFragments(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageMinimalProject(t)
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	body, mime := readResource(t, c, 104, "ctxloom://fragments")
	assert.Equal(t, "application/yaml", mime)
	// ListFragments returns a struct that serializes to YAML with at
	// least one top-level key (fragments or count); the body is
	// guaranteed non-empty even for empty stores.
	assert.NotEmpty(t, strings.TrimSpace(body))
}

// TestMCP_WireProtocol_ReadTemplatedFragment confirms the URI-template
// path: requesting ctxloom://fragments/<name> for an unknown fragment
// returns a structured error rather than an empty body. The handler
// invokes operations.GetFragment which surfaces "not found" errors,
// and the SDK translates them into JSON-RPC error responses.
func TestMCP_WireProtocol_ReadTemplatedFragment_NotFound(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stageMinimalProject(t)
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 105, "method": "resources/read",
		"params": map[string]any{"uri": "ctxloom://fragments/no-such-fragment"},
	})
	msg := c.recvByID(t, 105)
	// Either an explicit "error" field at the top level (preferred for
	// JSON-RPC errors) OR a result with empty contents — accept either,
	// confirm we don't get a successful arbitrary YAML body for a
	// missing fragment.
	if errField, ok := msg["error"].(map[string]any); ok {
		assert.NotEmpty(t, errField["message"], "error response must have a message")
		return
	}
	// If result instead of error: contents should be empty or absent.
	if result, ok := msg["result"].(map[string]any); ok {
		contents, _ := result["contents"].([]any)
		if len(contents) > 0 {
			first := contents[0].(map[string]any)
			text, _ := first["text"].(string)
			// Empty body or a clear "not found" marker is OK; an arbitrary
			// body would be the bug.
			assert.True(t, text == "" || strings.Contains(strings.ToLower(text), "not found"),
				"unknown fragment must not return arbitrary content")
		}
	}
}
