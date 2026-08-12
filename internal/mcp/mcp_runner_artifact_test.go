package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRunnerServer_ReportThenFetchArtifact drives the REAL tool-calling code
// path E1 added (reportHandler's publish_paths upload, fetchArtifactHandler's
// download+verify+place) through the runner's actual MCP surface, against a
// live coordinator — not just the coord package's lower-level Home API. This
// closes the loop the coord-package hermetic tests (upload/download
// mechanics, integrity, ownership) don't reach: cwd-safe path resolution,
// mimeByExt, and the mcp.Tool wiring itself (mcpschema.RouteArtifactFetch).
func TestRunnerServer_ReportThenFetchArtifact(t *testing.T) {
	cwd := testsupport.ProjectDir(t) // isolated HOME + cwd (forbidigo: no raw os.Chdir)

	c, err := coord.New(coord.Options{
		ProjectDir: cwd,
		StateDir:   t.TempDir(),
		Cfg:        testConfig(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, c.Serve())

	const harp = "owner-harp"
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)
	home, err := coord.NewHome(context.Background(), coord.HomeConfig{
		URL:     c.LoopbackURL(),
		Token:   token,
		RunID:   "", // depth-0 session owner
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })

	server, err := newRunnerMCPServer(testConfig(), harp, home, false, "")
	require.NoError(t, err)

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	// A file the agent explicitly names for publication (E1c's generic
	// case) — separate from the automatic *.plan.md stamping, which needs
	// no fixture here (planCandidates degrades to empty when the session
	// dir doesn't exist, exercised elsewhere).
	const relSource = "findings/coverage.json"
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, "findings"), 0o755))
	content := []byte(`{"lines_covered": 87, "note": "the containerized child's plan, retrievable after the container is gone"}`)
	require.NoError(t, os.WriteFile(filepath.Join(cwd, relSource), content, 0o644))

	reportArgs, err := json.Marshal(map[string]any{
		"scope":         "SCOPE_FINAL",
		"text":          "done; see the coverage artifact",
		"publish_paths": []string{relSource},
	})
	require.NoError(t, err)
	reportResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_report", Arguments: json.RawMessage(reportArgs)})
	require.NoError(t, err)
	require.False(t, reportResult.IsError, "agent_report failed: %+v", reportResult)

	var reportOut struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}
	require.NoError(t, decodeStructured(reportResult, &reportOut))
	require.Len(t, reportOut.ArtifactIDs, 1)
	artifactID := reportOut.ArtifactIDs[0]
	require.Equal(t, "file/"+relSource, artifactID)

	// Consume: fetch the SAME session's own artifact to a different local
	// path (self-fetch — agent_id == this session's own harp).
	const relDest = "retrieved/coverage.json"
	fetchArgs, err := json.Marshal(map[string]any{
		"agent_id":    harp,
		"artifact_id": artifactID,
		"dest_path":   relDest,
	})
	require.NoError(t, err)
	fetchResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_fetch_artifact", Arguments: json.RawMessage(fetchArgs)})
	require.NoError(t, err)
	require.False(t, fetchResult.IsError, "agent_fetch_artifact failed: %+v", fetchResult)

	got, err := os.ReadFile(filepath.Join(cwd, relDest))
	require.NoError(t, err)
	require.Equal(t, content, got, "fetched bytes must match what agent_report published, byte for byte")

	// A path escaping the working directory is rejected, never silently
	// clamped or resolved elsewhere.
	escapeArgs, err := json.Marshal(map[string]any{
		"agent_id":    harp,
		"artifact_id": artifactID,
		"dest_path":   "../escape.json",
	})
	require.NoError(t, err)
	_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_fetch_artifact", Arguments: json.RawMessage(escapeArgs)})
	require.Error(t, err, "a dest_path escaping the working directory must be rejected")
}

// TestRunnerServer_ArtifactPathsResolveAgainstCellWorkDir pins that the
// runner's cell-path boundary must anchor to the CELL work dir the harness
// actually runs in, not this process's own os.Getwd() (the coordinator's
// cwd) — the workspace:worktree shape where the runner
// is spawned with no cmd.Dir and inherits the coordinator's cwd while the
// harness's engine process runs with cmd.Dir=the per-agent worktree.
// Deliberately makes the two DIFFER, then proves publish_paths/dest_path
// resolve against the cell dir, never the coordinator's own cwd.
func TestRunnerServer_ArtifactPathsResolveAgainstCellWorkDir(t *testing.T) {
	coordCwd := testsupport.ProjectDir(t) // stands in for the coordinator's own cwd
	cellDir := t.TempDir()                // stands in for the per-agent worktree

	c, err := coord.New(coord.Options{
		ProjectDir: coordCwd,
		StateDir:   t.TempDir(),
		Cfg:        testConfig(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, c.Serve())

	const harp = "owner-harp"
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)
	home, err := coord.NewHome(context.Background(), coord.HomeConfig{
		URL:     c.LoopbackURL(),
		Token:   token,
		RunID:   "",
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })

	server, err := newRunnerMCPServer(testConfig(), harp, home, false, cellDir)
	require.NoError(t, err)

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	const relSource = "findings/coverage.json"
	require.NoError(t, os.MkdirAll(filepath.Join(cellDir, "findings"), 0o755))
	content := []byte(`{"lines_covered": 87}`)
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, relSource), content, 0o644))
	// Confirm it does NOT also exist under the coordinator's cwd (both dirs
	// are freshly minted, but this pins the setup's own precondition).
	_, statErr := os.Stat(filepath.Join(coordCwd, relSource))
	require.True(t, os.IsNotExist(statErr))

	reportArgs, err := json.Marshal(map[string]any{
		"scope":         "SCOPE_FINAL",
		"text":          "done",
		"publish_paths": []string{relSource},
	})
	require.NoError(t, err)
	reportResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_report", Arguments: json.RawMessage(reportArgs)})
	require.NoError(t, err)
	require.False(t, reportResult.IsError,
		"agent_report must resolve publish_paths against the CELL work dir (where the file actually is), "+
			"not the coordinator's own cwd: %+v", reportResult)

	var reportOut struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}
	require.NoError(t, decodeStructured(reportResult, &reportOut))
	require.Len(t, reportOut.ArtifactIDs, 1)
	artifactID := reportOut.ArtifactIDs[0]

	const relDest = "retrieved/coverage.json"
	fetchArgs, err := json.Marshal(map[string]any{
		"agent_id":    harp,
		"artifact_id": artifactID,
		"dest_path":   relDest,
	})
	require.NoError(t, err)
	fetchResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_fetch_artifact", Arguments: json.RawMessage(fetchArgs)})
	require.NoError(t, err)
	require.False(t, fetchResult.IsError, "agent_fetch_artifact failed: %+v", fetchResult)

	got, err := os.ReadFile(filepath.Join(cellDir, relDest))
	require.NoError(t, err, "the fetched artifact must be placed under the CELL work dir")
	require.Equal(t, content, got)

	_, statErr = os.Stat(filepath.Join(coordCwd, relDest))
	assert.True(t, os.IsNotExist(statErr), "the fetched artifact must NEVER be placed under the coordinator's own cwd")
}

// decodeStructured re-marshals a CallToolResult's StructuredContent (an
// any/map[string]any at this layer) into out.
func decodeStructured(res *mcp.CallToolResult, out any) error {
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
