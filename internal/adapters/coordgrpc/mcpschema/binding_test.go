package mcpschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every binding has a checked-in golden; every golden belongs to a binding;
// all parse and carry the essentials. (The regenerate-and-diff drift gate is
// `just gen-mcp-schemas-check` in CI — this guards the embedded runtime
// surface itself.)
func TestGoldens_MatchBindingTable(t *testing.T) {
	tools, err := Tools()
	require.NoError(t, err)

	byName := map[string]ToolSpec{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	bindings := CoordinationBindings()
	require.Len(t, tools, len(bindings), "one golden per binding, no strays")

	for _, b := range bindings {
		spec, ok := byName[b.Tool]
		require.True(t, ok, "missing golden for %s", b.Tool)
		assert.NotEmpty(t, spec.Description, "%s description", b.Tool)
		var in map[string]any
		require.NoError(t, json.Unmarshal(spec.InputSchema, &in), "%s input schema", b.Tool)
		assert.Equal(t, "object", in["type"], "%s input is an object schema", b.Tool)
		assert.Equal(t, false, in["additionalProperties"], "%s inputs are closed", b.Tool)
	}
}

// The generated agent_run surface carries the D3-annotated requireds — the
// one rule proto3 cannot express and the schema must.
func TestGoldens_AgentRunRequireds(t *testing.T) {
	spec, ok := ToolByName(ToolAgentRun)
	require.True(t, ok)
	var in struct {
		Required []string `json:"required"`
	}
	require.NoError(t, json.Unmarshal(spec.InputSchema, &in))
	assert.ElementsMatch(t, []string{"role", "input"}, in.Required)
}

// Every binding is classified in the routing table, and the classification
// matches the binding's own declared Route (the binding table and routing
// table cannot drift apart). Route's zero value is RouteCoordination, so
// every binding predating E1 (which never sets Route) is still required to
// route as coordination — only a binding that explicitly opts into a
// different route (e.g. agent_fetch_artifact's RouteArtifactFetch) may.
func TestRoutes_CoverCoordinationBindings(t *testing.T) {
	routes := Routes()
	for _, b := range CoordinationBindings() {
		route, ok := routes[b.Tool]
		require.True(t, ok, "binding %s missing from routing table", b.Tool)
		assert.Equal(t, b.Route, route, "binding %s routing table entry must match its declared Route", b.Tool)
	}
}
