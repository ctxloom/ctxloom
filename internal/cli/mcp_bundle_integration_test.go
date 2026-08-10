package cli

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

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// buildCtxloomBinary compiles the ctxloom CLI to a tempdir and returns the
// path. We rebuild per-test-run rather than relying on the user's $PATH so
// the integration test exercises the source under test, not a stale install.
func buildCtxloomBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wire-protocol integration test in -short mode")
	}

	// Walk up from this test file (internal/cli/) to the module root.
	_, file, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(file))) // internal/cli/ → repo root

	binDir := t.TempDir()
	binName := "ctxloom"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	// The ctxloom main package now lives at ./cmd/ctxloom (was the repo root).
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/ctxloom")
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

	// `mcp serve` is the machine surface — the same argv agent.CtxloomMCPArgs
	// writes into every engine's settings, so this harness drives exactly what
	// an engine drives.
	cmd := exec.CommandContext(ctx, binPath, agent.CtxloomMCPArgs...)
	cmd.Dir = workDir
	// Isolate the spawned server: a fresh tempdir HOME and scrubbed session env
	// so it never resolves a home-rooted store (tasks log, sessions index)
	// against the host's ~/.ctxloom or the ambient session's CTXLOOM_PROJECT_ID.
	cmd.Env = testsupport.ScrubbedEnv(t)
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
