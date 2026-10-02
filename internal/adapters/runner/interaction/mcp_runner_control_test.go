package interaction

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The five control tools (agent_steer / agent_ask / agent_summarize /
// agent_pause / agent_resume) on the runner surface: withheld from a leaf,
// served to a coordinator with their generated schemas, and terminating in
// the coordinator's control verbs over the ControlRun wire pair.

// controlTools is the set under test, spelled once.
var controlTools = []string{
	mcpschema.ToolAgentSteer,
	mcpschema.ToolAgentAsk,
	mcpschema.ToolAgentSummarize,
	mcpschema.ToolAgentPause,
	mcpschema.ToolAgentResume,
}

// TestRunnerServer_LeafIsRefusedEachControlTool is the leaf-gate refusal,
// one subtest per tool: a leaf has no children, so a control tool in its
// hands could only be refused by the coordinator — and a leaf holding one
// infers it has children to control, the stall the gate exists to prevent.
// The coordinator-capable runner serves every one of them.
func TestRunnerServer_LeafIsRefusedEachControlTool(t *testing.T) {
	leaf, err := newTestServer("leaf-harp", testHome(t), true, "")
	require.NoError(t, err)
	leafTools := listServerTools(t, leaf)
	coordinator, err := newTestServer("coord-harp", testHome(t), false, "")
	require.NoError(t, err)
	coordTools := listServerTools(t, coordinator)

	for _, name := range controlTools {
		t.Run(name, func(t *testing.T) {
			_, onLeaf := leafTools[name]
			assert.False(t, onLeaf, "a leaf runner must NOT register %s", name)
			assert.True(t, mcpschema.CoordinatorOnlyTools()[name], "%s must be classified coordinator-only, or the gate cannot withhold it", name)

			tool, ok := coordTools[name]
			require.True(t, ok, "a coordinator-capable runner must serve %s", name)
			spec, ok := mcpschema.ToolByName(name)
			require.True(t, ok)
			assert.Equal(t, spec.Description, tool.Description, "%s carries its generated description", name)
			var in struct {
				Required []string `json:"required"`
			}
			require.NoError(t, json.Unmarshal(spec.InputSchema, &in))
			assert.Contains(t, in.Required, "harp", "%s must require the target harp", name)
		})
	}
}

// TestRunnerServer_ControlToolsReachTheCoordinatorVerb drives each control
// tool through the runner's REAL MCP surface against a live coordinator and
// reads back the coordinator verb's own verdict: the verb's guard names the
// target as one this coordinator does not hold, and the wire edge names a
// missing argument. That is the whole chain — tool → typed ControlRun frame
// → serveControlRun → Coordinator.Control* — with no second orchestrator in
// between; the verbs' EFFECTS on a live child are pinned in the coord
// package's wire tests, where a scripted child engine exists.
func TestRunnerServer_ControlToolsReachTheCoordinatorVerb(t *testing.T) {
	cwd := testsupport.ProjectDir(t)
	c, err := coord.New(coord.Options{
		ProjectDir: cwd,
		StateDir:   t.TempDir(),
		Spawner:    spawn.New(nil, fixtureApp(t, testConfig()), cwd, nil),
		OwnerHarp:  "owner-harp",
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, coordgrpc.Serve(c))
	token, err := c.RegisterSessionOwner("owner-harp")
	require.NoError(t, err)
	home, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL: c.LoopbackURL(), Token: token, RunID: "", Harness: "mock", Version: "test", Harp: "owner-harp",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })

	server, err := newTestServer("owner-harp", home, false, "")
	require.NoError(t, err)
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	// A generated tool's handler returns its refusal as the call's error
	// (coordinationResult turns a non-OK status into an error), which the
	// runner surface reports as the tool call failing with that message.
	call := func(t *testing.T, name string, args map[string]any) string {
		t.Helper()
		raw, err := json.Marshal(args)
		require.NoError(t, err)
		_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(raw)})
		require.Error(t, err, "%s against a target this coordinator does not hold must be refused", name)
		return err.Error()
	}

	body := map[string]string{
		mcpschema.ToolAgentSteer:     "text",
		mcpschema.ToolAgentAsk:       "text",
		mcpschema.ToolAgentSummarize: "focus",
	}
	for _, name := range controlTools {
		t.Run(name+" reaches the verb's guard", func(t *testing.T) {
			args := map[string]any{"harp": "no-such-child"}
			if field, has := body[name]; has {
				args[field] = "hello?"
			}
			msg := call(t, name, args)
			assert.Contains(t, msg, name+": ", "the verdict names the tool")
			assert.Contains(t, msg, "not a child of this coordinator", "the verdict is the control verb's own guard (coord.controlTarget)")
		})
	}
	for name, field := range body {
		t.Run(name+" without its "+field+" is refused at the wire edge", func(t *testing.T) {
			msg := call(t, name, map[string]any{"harp": "no-such-child"})
			assert.Contains(t, msg, field+" is required")
		})
	}
	t.Run("an argument outside the schema is refused before the wire", func(t *testing.T) {
		msg := call(t, mcpschema.ToolAgentPause, map[string]any{"harp": "x", "run_id": "not-a-field"})
		assert.Contains(t, msg, "run_id")
	})
}
