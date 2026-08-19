package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// agentRunSurfaces returns agent_run's advertised input schema from BOTH
// surfaces, decoded: the generated (proto-canonical) one a runner-hosted
// engine reads, and the typed one the stdio server infers. They are produced
// independently, so nothing but a test makes them agree.
func agentRunSurfaces(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}

	generated, ok := mcpschema.ToolByName(mcpschema.ToolAgentRun)
	require.True(t, ok, "agent_run must have a generated schema")
	out["generated (proto-canonical, runner surface)"] = decodeSchema(t, generated.InputSchema)

	s := &ctxServer{cfg: testConfig()}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)
	stdioTool, ok := listServerTools(t, server)[mcpschema.ToolAgentRun]
	require.True(t, ok, "the stdio server must advertise agent_run")
	out["stdio (typed struct)"] = decodeSchema(t, stdioTool.InputSchema)

	return out
}

func decodeSchema(t *testing.T, schema any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

// agentRunEnum digs out the enum advertised for one per-call argument. The
// two surfaces nest it differently — the generated one wraps the arguments in
// agent_run's free-form `input` Struct, the stdio one is flat — so the lookup
// tries both rather than pinning one shape.
func agentRunEnum(t *testing.T, schema map[string]any, argument string) []string {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	require.NotNil(t, props, "the schema advertises properties at all")
	arg, ok := props[argument].(map[string]any)
	if !ok {
		input, _ := props["input"].(map[string]any)
		require.NotNil(t, input, "no %q argument and no input object to find it in", argument)
		inner, _ := input["properties"].(map[string]any)
		require.NotNil(t, inner, "agent_run's input object declares no properties — the per-call vocabularies are unconstrained")
		arg, ok = inner[argument].(map[string]any)
	}
	require.True(t, ok, "the schema declares the %q argument", argument)
	raw, ok := arg["enum"].([]any)
	require.True(t, ok, "the %q argument carries an enum, not just prose about its legal values", argument)
	members := make([]string, 0, len(raw))
	for _, m := range raw {
		s, ok := m.(string)
		require.True(t, ok)
		members = append(members, s)
	}
	return members
}

// TestAgentRun_ConstrainsPerCallVocabulariesOnBothSurfaces is the wire half of
// typing dirty_tree_handler. Both surfaces DESCRIBED the legal values in
// prose and constrained nothing, while the human-edited channel (the project
// config's JSON Schema) has carried enums for the same two keys all along —
// so the agent-driven per-call channel was the loose one, and its
// dirty_tree_handler default is the member that auto-commits the user's tree.
//
// The expectation comes from each vocabulary's OWNING package, so a member
// added there fails this test until both surfaces carry it.
func TestAgentRun_ConstrainsPerCallVocabulariesOnBothSurfaces(t *testing.T) {
	for name, schema := range agentRunSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, operations.DirtyTreeHandlerNames(), agentRunEnum(t, schema, "dirty_tree_handler"),
				"the advertised dirty_tree_handler enum must be the vocabulary operations owns")
			assert.Equal(t, isolation.WorkspaceNames(), agentRunEnum(t, schema, "workspace"),
				"the advertised workspace enum must be the vocabulary isolation owns")
		})
	}
}

// TestAgentRun_StdioSurfaceRefusesAnUnknownDirtyTreeHandler proves the stdio
// surface's enum is ENFORCED, not merely advertised: the SDK validates
// arguments against the advertised schema before the handler runs, so a
// typo'd handler is rejected at the tool call and never reaches the spawn.
func TestAgentRun_StdioSurfaceRefusesAnUnknownDirtyTreeHandler(t *testing.T) {
	s := &ctxServer{cfg: testConfig()}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	call := func(handler string) string {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: mcpschema.ToolAgentRun,
			Arguments: map[string]any{
				"agent":              "worker",
				"prompt":             "task",
				"dirty_tree_handler": handler,
			},
		})
		require.NoError(t, err, "the call completes; any refusal rides the result")
		require.True(t, res.IsError)
		return resultText(t, res)
	}

	// The vacuity guard: a DECLARED member gets past argument validation and
	// fails later, on this fixture's missing agent. So the refusal below is
	// the enum rejecting the spelling, not the call failing for its own
	// unrelated reasons.
	declared := call(string(operations.DirtyTreeHandlerStale))
	assert.NotContains(t, declared, "does not equal any of", "a declared member is not an argument-validation failure")

	typo := call("fial")
	assert.Contains(t, typo, "dirty_tree_handler")
	assert.Contains(t, typo, "fial")
	assert.Contains(t, typo, "does not equal any of", "the enum is ENFORCED before the handler runs, not merely advertised")
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	raw, err := json.Marshal(res.Content)
	require.NoError(t, err)
	return string(raw)
}
