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
	ToolAgentRecv: {request: nil, transportOnly: []string{"wait"}},
	ToolAgentStop: {request: coord.StopRequest{},
		// run_id is the wire's address; serveStopRun resolves it to the harp.
		fields: map[string]string{"run_id": "Harp", "reason": "Reason"}},
	ToolAgentReport: {request: coord.ReportRequest{},
		fields:        map[string]string{"scope": "Scope", "text": "Body"},
		transportOnly: []string{"artifact_ids", "covers_through_seq", "publish_paths", "step_id", "structured"}},
	ToolRoster: {request: nil, transportOnly: []string{"include_terminal", "role"}},
	ToolAgentFetchArtifact: {request: coord.FetchRequest{},
		fields:        map[string]string{"agent_id": "Harp", "artifact_id": "ArtifactID"},
		transportOnly: []string{"dest_path"}},
	ToolAgentSteer:     {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "text": "Body"}},
	ToolAgentAsk:       {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "text": "Body"}},
	ToolAgentSummarize: {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "focus": "Body"}},
	ToolAgentPause:     {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp", "reason": "Body"}},
	ToolAgentResume:    {request: coord.ControlRequest{}, fields: map[string]string{"harp": "Harp"}, unbound: []string{"Body"}},
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

		var in struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(tool.InputSchema, &in))
		var props []string
		for p := range in.Properties {
			props = append(props, p)
		}
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
		sort.Strings(props)
		sort.Strings(want)
		assert.Equal(t, want, props, "%s: the schema's properties are not the verb's projection", tool.Name)

		if b.request == nil {
			continue
		}
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
			assert.True(t, bound[f], "%s: %s.%s reaches no schema property", tool.Name, rt.Name(), f)
		}
		for _, f := range b.fields {
			_, has := rt.FieldByName(f)
			assert.True(t, has, "%s: binding names %s.%s, which does not exist", tool.Name, rt.Name(), f)
		}
	}
	for name := range verbBindings {
		assert.True(t, seen[name], "verbBindings names %q, which no generated schema carries", name)
	}
}
