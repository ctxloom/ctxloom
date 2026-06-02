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

	"github.com/stretchr/testify/require"
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
