package mcpschema_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	. "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// verbBinding says, for one generated tool, which of its input properties
// carry a field of the verb's request type (property → Go field) and which
// belong to the transport or the runner alone. The generated schema is a
// PROJECTION of the verb's request: the request type is the source, and
// this test is what holds the projection to it in both directions — a
// property added to the schema must name a field or be declared
// transport-only here, and a field added to a request must reach the schema.
type verbBinding struct {
	request       any               // the coord request type (nil: the verb takes scalars, not a request)
	fields        map[string]string // schema property → request field
	transportOnly []string          // properties the transport or runner consumes before the verb
	nested        string            // the property whose OBJECT carries fields (agent_run's free-form input)
	unbound       []string          // request fields this tool's verb does not take (resume carries no body)
}

var verbBindings = map[string]verbBinding{
	ToolAgentRun: {request: coord.SpawnRequest{}, nested: "input",
		fields: map[string]string{"role": "Agent", "prompt": "Prompt", "workspace": "Workspace", "dirty_tree_handler": "DirtyTree"}},
	ToolAgentSend: {request: coord.SendRequest{},
		// to_agent_id / to_role are the wire's two spellings of the one
		// recipient (sendRequestFromWire folds them).
		fields: map[string]string{"to_agent_id": "To", "to_role": "To", "text": "Body", "kind": "Kind", "structured": "Structured", "in_reply_to": "InReplyTo"}},
	ToolAgentStop: {request: coord.StopRequest{},
		// run_id is the wire's address; serveStopRun resolves it to the harp.
		fields: map[string]string{"run_id": "Harp", "reason": "Reason", "grace": "Grace"}},
	ToolAgentReport: {request: coord.ReportRequest{},
		fields:        map[string]string{"scope": "Scope", "text": "Body"},
		transportOnly: []string{"artifact_ids", "covers_through_seq", "publish_paths", "step_id", "structured"}},
	ToolRoster: {request: nil, transportOnly: []string{"include_terminal", "role"}},
	ToolAgentFetchArtifact: {request: coord.FetchRequest{},
		fields:        map[string]string{"agent_id": "Harp", "artifact_id": "ArtifactID"},
		transportOnly: []string{"dest_path"}},
	ToolAgentSteer: {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "text": "Body", "interrupt": "Interrupt"}},
	// Interrupt is the steer's alone: no other control verb cuts a turn short.
	ToolAgentAsk:       {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "text": "Body"}, unbound: []string{"Interrupt"}},
	ToolAgentSummarize: {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "focus": "Body"}, unbound: []string{"Interrupt"}},
	ToolAgentPause:     {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "reason": "Body"}, unbound: []string{"Interrupt"}},
	ToolAgentResume:    {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp"}, unbound: []string{"Body", "Interrupt"}},
}

// TestGeneratedSchemas_ProjectTheVerbRequests: every generated tool is bound
// to a verb here, its input properties are exactly the bound fields plus the
// declared transport-only ones, and every field of the request type it
// projects reaches the schema — except a ControlRequest's Verb, which the
// TOOL NAME carries.
func TestGeneratedSchemas_ProjectTheVerbRequests(t *testing.T) {
	tools, err := Tools()
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, tool := range tools {
		b, ok := verbBindings[tool.Name]
		require.True(t, ok, "generated tool %q is bound to no verb: add it to verbBindings", tool.Name)
		seen[tool.Name] = true
		assert.Equal(t, b.wantProperties(), schemaProperties(t, tool.InputSchema), "%s: the schema's properties are not the verb's projection", tool.Name)
		if b.request != nil {
			assertRequestProjected(t, tool.Name, b)
		}
	}
	for name := range verbBindings {
		assert.True(t, seen[name], "verbBindings names %q, which no generated schema carries", name)
	}
}

// schemaProperties is an input schema's property names, sorted.
func schemaProperties(t *testing.T, inputSchema json.RawMessage) []string {
	t.Helper()
	var in struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(inputSchema, &in))
	var props []string
	for p := range in.Properties {
		props = append(props, p)
	}
	sort.Strings(props)
	return props
}

// wantProperties is the properties the binding projects, sorted: its bound
// fields (only role at the top level when the rest nest under one object),
// the nesting object, and the transport-only properties.
func (b verbBinding) wantProperties() []string {
	var want []string
	for p := range b.fields {
		if b.nested == "" || p == "role" {
			want = append(want, p)
		}
	}
	if b.nested != "" {
		want = append(want, b.nested)
	}
	want = append(want, b.transportOnly...)
	sort.Strings(want)
	return want
}

// assertRequestProjected asserts every field of the binding's request type
// reaches a schema property (a ControlRequest's Verb excepted: the tool name
// IS the verb), and every field the binding names exists.
func assertRequestProjected(t *testing.T, toolName string, b verbBinding) {
	t.Helper()
	rt := reflect.TypeOf(b.request)
	bound := map[string]bool{}
	for _, f := range b.fields {
		bound[f] = true
	}
	for _, f := range b.unbound {
		bound[f] = true
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i).Name
		if rt == reflect.TypeOf(coord.ControlRequest{}) && f == "Verb" {
			continue // the tool name IS the verb
		}
		assert.True(t, bound[f], "%s: %s.%s reaches no schema property", toolName, rt.Name(), f)
	}
	for _, f := range b.fields {
		_, has := rt.FieldByName(f)
		assert.True(t, has, "%s: binding names %s.%s, which does not exist", toolName, rt.Name(), f)
	}
}
