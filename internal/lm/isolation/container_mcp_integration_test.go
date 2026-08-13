//go:build docker_integration

// dire-five's live cross-boundary proof: a container child's real backend
// Setup (claude-code) must materialize .mcp.json with the CONTAINER ctxloom
// binary path, not the host self-exec path — see container.go's
// MCPCommandOverride doc and cli/run.go's stamp of
// agent.MCPCommandOverrideEnv. Run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run MCPCommandOverride
package isolation

import (
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

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

const claudeStubIntegrationImage = "ctxloom-iso-itest-claudestub:latest"

// buildClaudeStubImage extends buildIntegrationImage's minimal image with a
// stub `claude` binary that just sleeps: real claude-code Setup delivers
// .mcp.json before Execute ever runs, and grpc/server.go's Run handler
// ALWAYS runs Cleanup (which strips the ctxloom-managed servers back out)
// immediately after Execute returns — so a claude that exits instantly (or
// is simply absent) leaves no observable window between "written" and
// "reverted" for a test running outside the same process. Sleeping gives the
// test a window to read the delivered file mid-flight, over the identical-
// path bind mount, while Run() is still blocked inside Execute.
func buildClaudeStubImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static ctxloom: %v\n%s", err, out)
	}

	dockerfile := "FROM alpine:latest\n" +
		"COPY ctxloom /usr/local/bin/ctxloom\n" +
		"RUN printf '#!/bin/sh\\nsleep 20\\n' > /usr/local/bin/claude && chmod +x /usr/local/bin/claude\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))

	img := exec.Command("docker", "build", "-t", claudeStubIntegrationImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build claude-stub image: %v\n%s", err, out)
	}
}

// TestContainerPolicy_MCPCommandOverride_EndToEnd drives a REAL claude-code
// backend's Setup inside a container (the SAME transport
// TestContainerPolicy_TransportEndToEnd proves, but with a backend that
// actually materializes an MCP surface instead of the mock's no-op Setup) and
// PAYLOAD-asserts the delivered .mcp.json: the ctxloom-managed server's
// `command` must be the in-container binary path (defaultContainerBinary),
// never a host-only path. This is the dire-five regression guard the plan's
// §8 test approach calls for — reading the delivered payload, not an exit
// code (the old stdout-grep-only check would have false-passed a child with
// zero working MCP tools).
func TestContainerPolicy_MCPCommandOverride_EndToEnd(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container MCP-surface integration test")
	buildClaudeStubImage(t)

	rt := SelectRuntime("docker")
	require.Equal(t, "docker", rt.Name(), "docker must be selected when available")
	pol := NewContainerFor(rt, "mock").WithImage(claudeStubIntegrationImage)

	// A real project dir, bind-mounted at its identical path — the host reads
	// .mcp.json straight back out of it while the container has it open (no
	// docker exec needed; that's exactly the identical-path bind-mount
	// property the container axis relies on).
	projectDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ws, err := pol.PrepareWorkspace(ctx, projectDir, "itest-mcp")
	require.NoError(t, err, "PrepareWorkspace must succeed with docker + the built image")

	client, err := pol.SpawnClient("claude-code", "", 0, ws, nil)
	require.NoError(t, err, "SpawnClient must bring up the claude-code plugin in a container and connect")
	t.Cleanup(func() { client.Kill() })

	// dire-five's actual host-side wiring: stamp the container's MCP command
	// override onto the run env exactly as cli/run.go does, mirroring
	// operations.MCPCommandOverrideForPolicy(pol).
	override := pol.MCPCommandOverride()
	require.NotEmpty(t, override, "a container policy must report a non-empty MCP command override")

	req := &pb.RunStart{
		Options: &pb.RunOptions{
			WorkDir:        ws.Dir(),
			Mode:           pb.ExecutionMode_ONESHOT,
			PermissionMode: "bypass",
			CellKind:       pb.CellKind_CELL_KIND_PROCESS_ISOLATED,
			Env:            map[string]string{agent.MCPCommandOverrideEnv: override},
		},
		ManagedConfig: pb.ManagedConfigToProto(&agent.ManagedConfig{}),
	}

	// Run blocks for the stub claude's ~20s sleep; give the test its own
	// shorter deadline and cancel it once we've read the file, which aborts
	// Execute early instead of waiting out the full sleep.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = client.Run(runCtx, req, nil, io.Discard, io.Discard, nil)
	}()

	mcpPath := filepath.Join(projectDir, ".mcp.json")
	var cfg struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(mcpPath)
		if err != nil || len(data) == 0 {
			return false
		}
		if jerr := json.Unmarshal(data, &cfg); jerr != nil {
			return false
		}
		_, ok := cfg.MCPServers[agent.MCPServerName]
		return ok
	}, 15*time.Second, 25*time.Millisecond,
		"Setup must deliver .mcp.json with the ctxloom server BEFORE Execute's stub-claude sleep ends (and before Cleanup, which runs right after Execute, strips it back out)")

	ctxloomServer := cfg.MCPServers[agent.MCPServerName]
	assert.Equal(t, override, ctxloomServer.Command,
		"the container child's .mcp.json must name the IN-CONTAINER ctxloom path — a host-only path here means the engine's `ctxloom mcp` stdio shim can never launch (dire-five: zero MCP tools)")
	assert.Equal(t, defaultContainerBinary, ctxloomServer.Command,
		"must match the container axis's single source of truth")

	// Abort the still-sleeping stub claude and let Cleanup run rather than
	// waiting out the full sleep.
	runCancel()
	<-runDone

	client.Kill()
	require.NoError(t, ws.Cleanup())
}
