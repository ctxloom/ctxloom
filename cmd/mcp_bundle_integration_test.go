package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

func (c *mcpClient) send(t *testing.T, msg map[string]interface{}) {
	t.Helper()
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	_, err = c.stdin.Write(append(data, '\n'))
	require.NoError(t, err)
}

// recvByID consumes lines from stdout until it finds a response with the
// given id; returns the parsed message. Notifications without an id are
// skipped.
func (c *mcpClient) recvByID(t *testing.T, id int) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				continue
			}
			t.Fatalf("read stdout: %v", err)
		}
		var msg map[string]interface{}
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
func extractToolResultJSON(t *testing.T, msg map[string]interface{}) map[string]interface{} {
	t.Helper()
	require.Nil(t, msg["error"], "tool call returned an error: %v", msg["error"])
	result, ok := msg["result"].(map[string]interface{})
	require.True(t, ok, "result is not a map: %v", msg)
	content, ok := result["content"].([]interface{})
	require.True(t, ok, "result.content is not an array")
	require.NotEmpty(t, content)
	first, ok := content[0].(map[string]interface{})
	require.True(t, ok)
	text, ok := first["text"].(string)
	require.True(t, ok)
	var inner map[string]interface{}
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
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "test", "version": "0"},
		},
	})
	_ = c.recvByID(t, 1)
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]interface{}{},
	})

	// tools/list — confirm the four bundle tools are exposed.
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]interface{}{},
	})
	resp := c.recvByID(t, 2)
	tools := resp["result"].(map[string]interface{})["tools"].([]interface{})
	names := make(map[string]bool)
	for _, raw := range tools {
		t := raw.(map[string]interface{})
		names[t["name"].(string)] = true
	}
	for _, expected := range []string{"create_bundle", "update_bundle", "delete_bundle", "push_bundle"} {
		assert.True(t, names[expected], "tool %q surfaces over MCP", expected)
	}

	// tools/call — create_bundle
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]interface{}{
			"name": "create_bundle",
			"arguments": map[string]interface{}{
				"name":        "wire-test",
				"description": "wire-protocol smoke",
				"fragments": map[string]interface{}{
					"intro": map[string]interface{}{
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
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]interface{}{
			"name": "push_bundle",
			"arguments": map[string]interface{}{
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
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]interface{}{
			"name": "delete_bundle",
			"arguments": map[string]interface{}{
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
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "test", "version": "0"},
		},
	})
	_ = c.recvByID(t, 1)
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]interface{}{},
	})

	// create_bundle without a name — operations layer rejects this.
	c.send(t, map[string]interface{}{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]interface{}{
			"name":      "create_bundle",
			"arguments": map[string]interface{}{},
		},
	})
	resp := c.recvByID(t, 2)
	// Either result.isError=true (MCP error envelope) or top-level error is fine;
	// either way the server stays up and returns a structured response.
	if result, ok := resp["result"].(map[string]interface{}); ok {
		// Error reported via isError flag inside content envelope.
		isError, _ := result["isError"].(bool)
		if isError {
			content, _ := result["content"].([]interface{})
			require.NotEmpty(t, content)
			first := content[0].(map[string]interface{})
			text, _ := first["text"].(string)
			assert.Contains(t, text, "name is required")
			return
		}
	}
	// Otherwise top-level error is acceptable.
	if _, ok := resp["error"]; ok {
		return
	}
	t.Fatalf("expected error response, got: %v", resp)
}

// silence unused-import warning when the integration test is the only consumer
var _ = fmt.Sprint
