package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// opencode implements the tool-less marker (marauding-magnolia, decision
// #5): the compile-time half of the proof.
var _ engine.ToolLess = Opencode{}

// bindToolLess binds s through the marker. s's own posture is auto with a
// rule that allows a tool, on a trusted repository — none of which the
// tool-less launch may read.
func bindToolLess(t *testing.T) *instance {
	t.Helper()
	s := session(t, qwenBody())
	s.Trust = engine.TrustTrusted
	s.Permission = engine.PermissionPolicy{Posture: engine.Posture{Engine: EngineName, Document: map[string]any{
		keyMode: modeAuto, keyPermission: map[string]any{"bash": "allow"},
	}}}
	kind, err := Build()
	require.NoError(t, err)
	inst, err := kind.(engine.ToolLess).ToolLess(s)
	require.NoError(t, err)
	return inst.(*instance)
}

// The exact launch opencode's tool-less instance emits: the structured
// protocol without --auto, OPENCODE_PERMISSION denying every tool (opencode
// removes a tool whose last matching rule is a `*` deny from what it offers
// the model), the project's own config off even on a trusted repository (so
// no rule of its can follow the deny), and a generated config naming no MCP
// server and no permission of its own.
func TestToolLess_Exec(t *testing.T) {
	ex, err := bindToolLess(t).Exec(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "--format", "json", "--thinking", "--model", "ctxloom/qwen3.8-27b"}, ex.Args)
	_, err = structuredGrammar.ParseArgv(ex.Args)
	require.NoError(t, err)
	assert.Equal(t, `{"*":"deny"}`, ex.Env[envPermission])
	assert.Equal(t, "1", ex.Env[envDisableProjectConfig], "a trusted repository's own config could follow the deny")
	doc := configDoc(t, ex)
	assert.NotContains(t, doc, "mcp", "the generated config names no MCP server")
	assert.NotContains(t, doc, "permission")
}

// Every turn is the same launch: no --auto and the deny-all, whatever
// posture the turn carries — even one outside opencode's vocabulary, since
// the tool-less launch reads none.
func TestToolLess_EveryTurnDeniesEveryTool(t *testing.T) {
	inst := bindToolLess(t)
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	for _, mode := range []string{"", modeAuto, modeBuild, modePlan, "yolo"} {
		in := engine.Turn{Resume: "ses_1", Posture: engine.TurnPosture{Mode: mode}}
		args, err := inst.turnArgv(ex, in)
		require.NoError(t, err, mode)
		assert.Equal(t, []string{"run", "--format", "json", "--thinking", "--model", "ctxloom/qwen3.8-27b", "--session", "ses_1"}, args, mode)
		env, err := inst.turnEnv(ex, in)
		require.NoError(t, err, mode)
		assert.Equal(t, map[string]string{envPermission: `{"*":"deny"}`}, env, mode)
	}
}
