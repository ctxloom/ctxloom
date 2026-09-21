package mcp

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/coordtest"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ONE coordinator per project, hosted by a RUNNER. The stdio `ctxloom mcp`
// shim is a pure client of that runner: with a runner it forwards, without
// one it fails loudly — it never constructs a coordinator itself. These two
// tests are the gate on that boundary, and both assert on the FILESYSTEM:
// the coordinator's persisted state (the exclusive-owner lock and the
// journals under the project state dir) is the only evidence that a
// coordinator was stood up that does not depend on what the code SAID it
// did. A log line proves the message, not the absence of the write.
//
// What these tests cannot prove: the invariant is also a two-PROCESS
// property (two `ctxloom` processes hosting a coordinator for one project →
// the second is refused). An in-process gate reaches only the shim's own
// construction path; tests/integration's owner-refusal test covers the
// other.

// coordinatorStateOnDisk lists every owner lock and journal file under the
// isolated HOME's coordinator state root. Empty means no coordinator ever
// claimed a project state dir in this process.
func coordinatorStateOnDisk(t *testing.T) []string {
	t.Helper()
	root, err := paths.HomeCoordDir()
	require.NoError(t, err)
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == coord.OwnerLockFileName || strings.HasSuffix(d.Name(), ".jsonl") {
			found = append(found, path)
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

// driveTool connects an in-memory client to server and calls one tool,
// returning the result. err is the TRANSPORT error; a refusal rides the
// result's IsError. The call is bounded so a tool that blocks on an
// unreachable far side cannot hang the gate.
func driveTool(t *testing.T, server *mcp.Server, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

// TestBareShim_NoRunner_RefusesAndConstructsNoCoordinator: a bare local
// `ctxloom mcp` (no CTXLOOM_MCP_SOCKET — the shape an engine launched
// outside any `ctxloom run` would reach) with no runner must refuse every
// agent tool with the named sentinel, and must leave NO coordinator state
// on disk: no owner lock, no journals. Asserted on the payload, not the
// message, because this codebase's characteristic failure is exit 0 with
// the wrong side effect.
func TestBareShim_NoRunner_RefusesAndConstructsNoCoordinator(t *testing.T) {
	root := testsupport.ProjectDir(t) // isolated HOME + cwd; every CTXLOOM_* var cleared
	resetStrictness(t)
	// build is the composition root's constructor as `ctxloom run` composes it
	// (a real spawner), handed in exactly as ServeStdio hands cli.NewCoordinator.
	build := func(o coord.Options) (*coord.Coordinator, error) {
		runners := coordtest.NewRunners()
		t.Cleanup(runners.Close)
		o.Spawner = spawn.New(nil, fixtureApp(t, testConfig()), root, runners.Starter)
		c, err := coord.New(o)
		if err == nil {
			t.Cleanup(c.Close)
		}
		return c, err
	}
	s := &ctxServer{cfg: testConfig(), app: fixtureApp(t, testConfig()), build: build}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)

	calls := map[string]map[string]any{
		mcpschema.ToolAgentRun:  {"agent": "worker", "prompt": "task"},
		mcpschema.ToolAgentSend: {"to": "someone", "body": "hi", "kind": "message"},
	}
	for name, args := range calls {
		res, err := driveTool(t, server, name, args)
		require.NoError(t, err, "%s: the call completes; the refusal rides the result", name)
		require.True(t, res.IsError, "%s must refuse: there is no runner to forward to", name)
		assert.Contains(t, resultText(t, res), errNoRunner.Error(), "%s must name the sentinel so the remedy is followable", name)
	}

	assert.Nil(t, s.agents, "the shim must hold no coordinator after the refusal")
	assert.Empty(t, coordinatorStateOnDisk(t), "a shim with no runner must not claim a project state dir: no owner lock, no journals")
}

// TestShimWithRunner_ForwardsAndConstructsNothing is the positive twin: with
// a runner reachable, the shim's whole surface is the runner's mirror, and
// driving an agent tool through it leaves no coordinator state on disk in
// THIS process — the coordinator, if any, is the runner's to reach, never
// the shim's to build.
func TestShimWithRunner_ForwardsAndConstructsNothing(t *testing.T) {
	testsupport.ProjectDir(t)
	captureWarnings(t) // testHome's reconnect loop warns; keep it off os.Stderr
	endpoint, err := ServeRunnerMCP(report.Discard, testConfig(), "runner-harp", testHome(t))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	trigger := forwardTrigger{Name: coord.EnvMCPSocket}
	cs, outcome, err := prepareForward(context.Background(), trigger, endpoint.SocketPath)
	require.NoError(t, err)
	require.Equal(t, forwardOutcomeServed, outcome)
	t.Cleanup(func() { _ = cs.Close() })
	server, err := buildForwardServer(context.Background(), cs)
	require.NoError(t, err)

	// The outcome of the relayed call is the runner's (its Home points at a
	// dead endpoint, so it fails); what this gate asserts is that the SHIM
	// answered by forwarding, and built nothing of its own to answer with.
	_, _ = driveTool(t, server, mcpschema.ToolAgentRun, map[string]any{"role": "worker", "input": map[string]any{"prompt": "task"}})

	assert.Empty(t, coordinatorStateOnDisk(t), "a forwarding shim must leave no owner lock and no journals behind")
}
