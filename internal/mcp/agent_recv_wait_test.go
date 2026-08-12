package mcp

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// agent_recv's `wait` contract is advertised on two surfaces and enforced by two
// handlers. A model reads the advertised default and maximum and plans around
// them, so an enforcing clamp that disagrees with the advertised numbers is a
// silent lie: the caller asks for 600 seconds, is told that is allowed, and is
// cut off earlier. These tests hold all of it to one declaration.

// TestAgentRecvWait_AdvertisedNumbersMatchTheEnforcedClamp is the load-bearing
// one: the schema text is prose, the clamp is arithmetic, and nothing but this
// connects them.
func TestAgentRecvWait_AdvertisedNumbersMatchTheEnforcedClamp(t *testing.T) {
	assert.Equal(t, mcpschema.RecvWaitDefault, mcpschema.ClampRecvWait(0),
		"an absent or zero wait must resolve to the advertised default")
	assert.Equal(t, mcpschema.RecvWaitDefault, mcpschema.ClampRecvWait(-5),
		"a negative wait must resolve to the default, never to zero")
	assert.Equal(t, mcpschema.RecvWaitMax, mcpschema.ClampRecvWait(100000),
		"a wait past the maximum must be clamped to the advertised maximum")
	assert.Equal(t, 30*time.Second, mcpschema.ClampRecvWait(30),
		"a wait inside the bounds is honoured as seconds")
	assert.Equal(t, mcpschema.RecvWaitMax, mcpschema.ClampRecvWait(int(mcpschema.RecvWaitMax.Seconds())),
		"exactly the maximum is allowed, not clamped below it")
}

// TestAgentRecvWait_GeneratedSchemaDescribesTheRealBounds reads the description
// the runner surface actually advertises out of the generated tool schema.
func TestAgentRecvWait_GeneratedSchemaDescribesTheRealBounds(t *testing.T) {
	tools, err := mcpschema.Tools()
	require.NoError(t, err)

	var raw json.RawMessage
	for _, spec := range tools {
		if spec.Name == mcpschema.ToolAgentRecv {
			raw = spec.InputSchema
		}
	}
	require.NotEmpty(t, raw, "agent_recv must have a generated input schema")

	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	assert.Equal(t, mcpschema.RecvWaitDoc, schema.Properties["wait"].Description,
		"the advertised wait description must be the one declared alongside the bounds it quotes")
}

// TestAgentRecvWait_StdioSchemaDescribesTheSameBounds: the stdio surface's
// description rides a struct tag, which Go requires to be a literal — so it
// cannot reference the constant and is pinned here instead.
func TestAgentRecvWait_StdioSchemaDescribesTheSameBounds(t *testing.T) {
	field, ok := reflect.TypeOf(agentRecvInput{}).FieldByName("Wait")
	require.True(t, ok)
	assert.Equal(t, mcpschema.RecvWaitDoc, field.Tag.Get("jsonschema"),
		"the stdio surface must advertise the same wait contract as the runner surface; update the tag to match mcpschema.RecvWaitDoc")
}
