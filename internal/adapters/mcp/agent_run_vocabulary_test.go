package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
)

// agentRunSchema returns agent_run's advertised input schema, decoded: the
// generated (proto-canonical) one the session endpoint serves.
func agentRunSchema(t *testing.T) map[string]any {
	t.Helper()
	generated, ok := mcpschema.ToolByName(mcpschema.ToolAgentRun)
	require.True(t, ok, "agent_run must have a generated schema")
	return decodeSchema(t, generated.InputSchema)
}

func decodeSchema(t *testing.T, schema any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

// agentRunEnum digs out the enum advertised for one per-call argument: the
// generated schema wraps the arguments in agent_run's free-form `input`
// Struct, so the lookup descends into it.
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

// TestAgentRun_ConstrainsPerCallVocabularies: the per-call workspace
// argument carries an enum, not just prose about its legal values, matching
// the human-edited channel (the project config's JSON Schema).
//
// The expectation comes from the vocabulary's OWNING package, so a member
// added there fails this test until the surface carries it.
func TestAgentRun_ConstrainsPerCallVocabularies(t *testing.T) {
	schema := agentRunSchema(t)
	assert.Equal(t, isolation.WorkspaceNames(), agentRunEnum(t, schema, "workspace"),
		"the advertised workspace enum must be the vocabulary isolation owns")
}
