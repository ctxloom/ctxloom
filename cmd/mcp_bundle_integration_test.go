package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// buildCtxloomBinary compiles the ctxloom CLI to a tempdir and returns the
// path. We rebuild per-test-run rather than relying on the user's $PATH so
// the integration test exercises the source under test, not a stale install.
func buildCtxloomBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wire-protocol integration test in -short mode")
	}

	// Walk up from this test file to the module root (where main.go lives).
	_, file, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Dir(filepath.Dir(file)) // cmd/ → repo root

	binDir := t.TempDir()
	binName := "ctxloom"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return binPath
}

// mcpClient wraps the subprocess + its stdin/stdout for JSON-RPC exchanges.
type mcpClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func startMCPServer(t *testing.T, binPath, workDir string) *mcpClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, binPath, "mcp")
	cmd.Dir = workDir
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr // surface ctxloom warnings to the test log

	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })

	return &mcpClient{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
}

func (c *mcpClient) send(t *testing.T, msg map[string]any) {
	t.Helper()
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	_, err = c.stdin.Write(append(data, '\n'))
	require.NoError(t, err)
}

// recvByID consumes lines from stdout until it finds a response with the
// given id; returns the parsed message. Notifications without an id are
// skipped.
func (c *mcpClient) recvByID(t *testing.T, id int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			t.Fatalf("read stdout (id=%d): %v", id, err)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // skip non-JSON noise
		}
		if rid, ok := msg["id"].(float64); ok && int(rid) == id {
			return msg
		}
	}
	t.Fatalf("timeout waiting for response with id=%d", id)
	return nil
}

// extractToolResultJSON pulls the inner JSON from an MCP tool result —
// MCP wraps tool returns in a content[]{type:text, text:<json>} envelope.
func extractToolResultJSON(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	require.Nil(t, msg["error"], "tool call returned an error: %v", msg["error"])
	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "result is not a map: %v", msg)
	content, ok := result["content"].([]any)
	require.True(t, ok, "result.content is not an array")
	require.NotEmpty(t, content)
	first, ok := content[0].(map[string]any)
	require.True(t, ok)
	text, ok := first["text"].(string)
	require.True(t, ok)
	var inner map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &inner))
	return inner
}

// TestMCP_WireProtocol_BundleLifecycle exercises the FULL stdio JSON-RPC path
// (subprocess invocation, initialize handshake, tools/call dispatch, result
// envelope) for create_bundle → push_bundle (dry-run) → delete_bundle.
func TestMCP_WireProtocol_BundleLifecycle(t *testing.T) {
	binPath := buildCtxloomBinary(t)

	// Stage a project with a .ctxloom dir + remote config.
	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.BundlesPath(appDir), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"), []byte(`default: personal
remotes:
  personal:
    url: https://github.com/example/personal-bundles
    version: v1
`), 0644))

	c := startMCPServer(t, binPath, workDir)

	// initialize / initialized handshake
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

	// tools/list — confirm the four bundle tools are exposed.
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	})
	resp := c.recvByID(t, 2)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	names := make(map[string]bool)
	for _, raw := range tools {
		t := raw.(map[string]any)
		names[t["name"].(string)] = true
	}
	for _, expected := range []string{"create_bundle", "update_bundle", "delete_bundle", "push_bundle"} {
		assert.True(t, names[expected], "tool %q surfaces over MCP", expected)
	}

	// tools/call — create_bundle
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name": "create_bundle",
			"arguments": map[string]any{
				"name":        "wire-test",
				"description": "wire-protocol smoke",
				"fragments": map[string]any{
					"intro": map[string]any{
						"content":    "hi",
						"no_distill": true,
					},
				},
			},
		},
	})
	created := extractToolResultJSON(t, c.recvByID(t, 3))
	assert.Equal(t, "created", created["status"])

	bundlePath := filepath.Join(paths.BundlesPath(appDir), "wire-test.yaml")
	_, err := os.Stat(bundlePath)
	require.NoError(t, err, "bundle file landed on disk via the wire path")

	// tools/call — push_bundle dry-run
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name": "push_bundle",
			"arguments": map[string]any{
				"path":    bundlePath,
				"dry_run": true,
			},
		},
	})
	pushed := extractToolResultJSON(t, c.recvByID(t, 4))
	assert.Equal(t, "preview", pushed["status"])
	assert.Equal(t, "personal", pushed["remote"])
	assert.Equal(t, "ctxloom/v1/bundles/wire-test.yaml", pushed["target_path"])
	assert.NotEmpty(t, pushed["preview"])

	// tools/call — delete_bundle
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{
			"name": "delete_bundle",
			"arguments": map[string]any{
				"name": "wire-test",
			},
		},
	})
	deleted := extractToolResultJSON(t, c.recvByID(t, 5))
	assert.Equal(t, "deleted", deleted["status"])

	_, err = os.Stat(bundlePath)
	assert.True(t, os.IsNotExist(err), "delete_bundle removed the file")
}

// TestMCP_WireProtocol_InvalidArgs_ReturnsError checks that bad arguments
// surface as a JSON-RPC error result rather than crashing the server.
func TestMCP_WireProtocol_InvalidArgs_ReturnsError(t *testing.T) {
	binPath := buildCtxloomBinary(t)

	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.BundlesPath(appDir), 0755))

	c := startMCPServer(t, binPath, workDir)
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

	// create_bundle without a name — operations layer rejects this.
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "create_bundle",
			"arguments": map[string]any{},
		},
	})
	resp := c.recvByID(t, 2)
	// The SDK validates input against the tool's inferred JSON Schema
	// before calling the handler. A missing required property produces
	// this exact error message via the MCP isError envelope. We assert
	// the full string — if a future SDK version reworks the wording,
	// any downstream tooling that greps these errors needs to know, so
	// the test should fail loudly rather than silently drift.
	const wantErr = `validating "arguments": validating root: required: missing properties: ["name"]`

	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected result envelope, got: %v", resp)
	isError, _ := result["isError"].(bool)
	require.True(t, isError, "expected isError=true for schema validation failure, got: %v", result)

	content, _ := result["content"].([]any)
	require.NotEmpty(t, content, "isError result must include content")
	first, _ := content[0].(map[string]any)
	require.NotNil(t, first, "first content block must be a map")

	text, _ := first["text"].(string)
	assert.Equal(t, wantErr, text,
		"SDK schema-validation error wording is part of the contract — update this test if the SDK changes the message")
}

