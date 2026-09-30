package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/agents"
)

func TestPermissions_DecodesEveryField(t *testing.T) {
	var a agents.Agent
	require.NoError(t, yaml.Unmarshal([]byte(`
permissions:
  mode: plan
  after_plan: acceptEdits
  allow: ["Bash(npm test)", Read]
  deny: ["Bash(rm *)"]
  ask: [WebFetch]
  approver: human
  approval_timeout: 20m
`), &a))
	assert.Equal(t, agents.Permissions{
		Mode: "plan", AfterPlan: "acceptEdits",
		Allow: []string{"Bash(npm test)", "Read"}, Deny: []string{"Bash(rm *)"}, Ask: []string{"WebFetch"},
		Approver: "human", ApprovalTimeout: "20m",
	}, a.Permissions)
}

// A permissions block is a privilege grant: a misspelled key must not be
// dropped in silence (a `dney:` that vanished would leave the denial
// undeclared), so an unknown key refuses the whole document, naming the
// known ones.
func TestPermissions_RefusesAnUnknownKey(t *testing.T) {
	var a agents.Agent
	err := yaml.Unmarshal([]byte("permissions:\n  mode: plan\n  dney: [Bash]\n"), &a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"dney"`)
	assert.Contains(t, err.Error(), "mode, after_plan, allow, deny, ask, approver, approval_timeout")
}

func TestPermissions_RefusesAScalar(t *testing.T) {
	var a agents.Agent
	err := yaml.Unmarshal([]byte("permissions: plan\n"), &a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permissions: {mode: plan}", "the refusal says what to write")
}

func TestPermissions_EmptyIsUndeclared(t *testing.T) {
	var a agents.Agent
	require.NoError(t, yaml.Unmarshal([]byte("llm: x\n"), &a))
	assert.True(t, a.Permissions.IsZero())
	assert.False(t, agents.Permissions{Deny: []string{"Bash"}}.IsZero())

	out, err := yaml.Marshal(agents.Agent{LLM: "x"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "permissions", "an undeclared block is not written")
	out, err = yaml.Marshal(agents.Agent{Permissions: agents.Permissions{Mode: "plan"}})
	require.NoError(t, err)
	assert.Contains(t, string(out), "permissions:\n    mode: plan\n")
}

func TestPermissions_String(t *testing.T) {
	assert.Equal(t, "", agents.Permissions{}.String())
	assert.Equal(t, "plan", agents.Permissions{Mode: "plan"}.String())
	assert.Equal(t, "plan, after_plan=acceptEdits, allow=[Read, Bash(npm test)], deny=[Bash(rm *)], ask=[WebFetch], approver=none, approval_timeout=20m",
		agents.Permissions{Mode: "plan", AfterPlan: "acceptEdits", Allow: []string{"Read", "Bash(npm test)"}, Deny: []string{"Bash(rm *)"}, Ask: []string{"WebFetch"}, Approver: "none", ApprovalTimeout: "20m"}.String())
	assert.Equal(t, "deny=[Bash]", agents.Permissions{Deny: []string{"Bash"}}.String())
}
