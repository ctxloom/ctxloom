package mcp

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// generatedToolHandler's own doc promises "An unclassified tool is a
// startup error, never a silent fallthrough". It was not: the route arrived
// from a bare map lookup, and mcpschema.Route's zero value IS
// RouteCoordination — so a generated tool missing from the routing table was
// handed a coordination handler and served, indistinguishable from one
// deliberately classified as coordination. The routing table is the only thing
// deciding where a tool's arguments go.
func TestRegisterGeneratedTools_UnclassifiedToolIsAStartupError(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	err := registerGeneratedTools(server, testHome(t), "test-harp", t.TempDir(), false,
		map[string]mcpschema.Route{}, map[string]bool{})
	require.Error(t, err, "an unclassified generated tool must fail runner startup")
	assert.Contains(t, err.Error(), "mcpschema.Routes", "the error points at the table to fix")
}

// A leaf withholds the coordinator-only tools, but it still vouches for the
// surface it serves — an unclassified tool is not excused by being withheld.
func TestRegisterGeneratedTools_UnclassifiedIsAnErrorForALeafToo(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	err := registerGeneratedTools(server, testHome(t), "test-harp", t.TempDir(), true,
		map[string]mcpschema.Route{}, map[string]bool{})
	require.Error(t, err, "a leaf runner must fail on an unclassified tool as well")
}

// The real routing table still registers the whole generated surface.
func TestRegisterGeneratedTools_ClassifiedSurfaceRegisters(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	registered := map[string]bool{}
	require.NoError(t, registerGeneratedTools(server, testHome(t), "test-harp", t.TempDir(), false,
		mcpschema.Routes(), registered))

	specs, err := mcpschema.Tools()
	require.NoError(t, err)
	for _, spec := range specs {
		assert.True(t, registered[spec.Name], "generated tool %s registered", spec.Name)
	}
}

// agent_report's ADVERTISED output schema is generated from the
// binding table in mcpschema; the payload reportResult actually returns is a
// hand-written Go map in this package. Nothing tied the two together, and the
// MCP SDK does not validate structured content against the declared schema —
// so a key renamed on either side would ship a tool whose declared and
// delivered output shapes disagree, with the model believing the declaration.
func TestReportResult_DeliversExactlyTheAdvertisedOutputKeys(t *testing.T) {
	spec, ok := mcpschema.ToolByName("agent_report")
	require.True(t, ok, "agent_report has a generated golden")
	require.NotEmpty(t, spec.OutputSchema, "agent_report advertises an output schema")

	var out struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(spec.OutputSchema, &out))
	require.NotEmpty(t, out.Properties, "the advertised output schema names properties")

	for _, artifacts := range [][]*agentcoordpb.ArtifactProduced{
		nil,
		{{ArtifactId: "plan/one"}},
	} {
		res := reportResult(artifacts, nil)
		structured, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok, "agent_report returns structured content")
		assert.Equal(t,
			slices.Sorted(maps.Keys(out.Properties)),
			slices.Sorted(maps.Keys(structured)),
			"the keys agent_report delivers must be exactly the keys its generated schema advertises")
	}
}
