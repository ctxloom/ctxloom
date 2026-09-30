package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/agents"
)

func ptr[T any](v T) *T { return &v }

// A binding's block: the neutral fields, and one block per engine keyed by
// the engine's name — a binding's engine is known only at resolve, and one
// binding may carry blocks for several.
func TestPermissions_BindingDecodesNeutralFieldsAndEngineBlocks(t *testing.T) {
	var a agents.Agent
	require.NoError(t, yaml.Unmarshal([]byte(`
permissions:
  approver: reviewer
  approval_timeout: 20m
  sandbox: workspace-write
  network: false
  claude-code:
    mode: plan
    after_plan: acceptEdits
    deny: ["Bash(rm *)"]
  mock:
    mode: bypass
`), &a))
	assert.Equal(t, agents.NeutralPermissions{Approver: "reviewer", ApprovalTimeout: "20m", Sandbox: "workspace-write", Network: ptr(false)}, a.Permissions.NeutralPermissions)
	assert.Equal(t, map[string]map[string]any{
		"claude-code": {"mode": "plan", "after_plan": "acceptEdits", "deny": []any{"Bash(rm *)"}},
		"mock":        {"mode": "bypass"},
	}, a.Permissions.Engines)
}

func TestPermissions_BindingRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"scalar":             {"permissions: plan\n", "permissions: {<engine>: {mode: plan}}"},
		"engine key a value": {"permissions:\n  mode: plan\n", `"mode" is not a neutral key`},
		"network not a bool": {"permissions:\n  network: maybe\n", "network"},
	} {
		var a agents.Agent
		err := yaml.Unmarshal([]byte(tc.doc), &a)
		if assert.Errorf(t, err, "%s", name) {
			assert.Contains(t, err.Error(), tc.want, name)
		}
	}
}

// A label names its engine by its type, so its engine's keys sit flat
// beside the neutral fields.
func TestLabelPermissions_DecodesFlat(t *testing.T) {
	var l agents.LabelPermissions
	require.NoError(t, yaml.Unmarshal([]byte("mode: acceptEdits\ndeny: [Bash]\nsandbox: full\n"), &l))
	assert.Equal(t, agents.NeutralPermissions{Sandbox: "full"}, l.NeutralPermissions)
	assert.Equal(t, map[string]any{"mode": "acceptEdits", "deny": []any{"Bash"}}, l.Engine)

	var bad agents.LabelPermissions
	assert.Error(t, yaml.Unmarshal([]byte("plan\n"), &bad), "a scalar is refused")
}

// The project knows no engine: it takes only the neutral fields, and a
// mode or a rule there is refused naming where it belongs.
func TestProjectPermissions_NeutralOnly(t *testing.T) {
	var p agents.NeutralPermissions
	require.NoError(t, yaml.Unmarshal([]byte("approver: none\nsandbox: read-only\n"), &p))
	assert.Equal(t, agents.NeutralPermissions{Approver: "none", Sandbox: "read-only"}, p)
	for _, key := range []string{"mode", "deny", "allow", "ask", "after_plan"} {
		var q agents.NeutralPermissions
		err := yaml.Unmarshal([]byte(key+": x\n"), &q)
		require.Errorf(t, err, "%s", key)
		assert.Contains(t, err.Error(), "agents.<name>.permissions.<engine>."+key, key)
		assert.Contains(t, err.Error(), "llm.configs.<label>.permissions."+key, key)
	}
}

func TestPermissions_RoundTripsAndIsZero(t *testing.T) {
	assert.True(t, agents.Permissions{}.IsZero())
	p := agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "none"}, Engines: map[string]map[string]any{"mock": {"mode": "plan"}}}
	out, err := yaml.Marshal(agents.Agent{Permissions: p})
	require.NoError(t, err)
	var back agents.Agent
	require.NoError(t, yaml.Unmarshal(out, &back))
	assert.Equal(t, p, back.Permissions)
	out, err = yaml.Marshal(agents.Agent{LLM: "x"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "permissions")

	c := p.Clone()
	c.Engines["mock"]["mode"] = "bypass"
	assert.Equal(t, "plan", p.Engines["mock"]["mode"], "a clone never aliases an engine block")
}

func TestPermissions_String(t *testing.T) {
	assert.Equal(t, "", agents.Permissions{}.String())
	p := agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "none", Network: ptr(false)}, Engines: map[string]map[string]any{"mock": {"mode": "plan"}, "claude-code": {"deny": []any{"Bash"}, "mode": "default"}}}
	assert.Equal(t, "approver=none, network=false, claude-code={deny=[Bash], mode=default}, mock={mode=plan}", p.String())
}

// may_delegate: unset or empty permits any delegation; a list names the
// roles this agent may launch.
func TestAgent_MayDelegate(t *testing.T) {
	var a agents.Agent
	require.NoError(t, yaml.Unmarshal([]byte("may_delegate: [finder, reviewer]\n"), &a))
	assert.Equal(t, []string{"finder", "reviewer"}, a.MayDelegate)
	assert.True(t, a.Delegates("finder"))
	assert.False(t, a.Delegates("developer"))
	assert.True(t, agents.Agent{}.Delegates("anything"), "unset permits any role")
	assert.True(t, agents.Agent{MayDelegate: []string{}}.Delegates("anything"), "empty permits any role")
}
