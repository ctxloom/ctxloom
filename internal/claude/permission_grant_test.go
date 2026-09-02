package claude

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestPlanGrantsEveryAttachedMCPServer asserts the grant half of the plan-mode
// gate, including COMPANION servers.
//
// readOnlyHint alone is not enough: without a grant a plan agent's call is
// refused "requested permissions ... but you haven't granted it yet". The
// grant names each attached SERVER, which is the only form that can cover a
// companion whose tool inventory ctxloom does not know.
func TestPlanGrantsEveryAttachedMCPServer(t *testing.T) {
	args := permissionArgs(agent.PermissionPlan, []string{"ctxloom", "taskloom", "serena"})

	granted := grantedTools(t, args)
	assert.ElementsMatch(t,
		[]string{"mcp__ctxloom", "mcp__taskloom", "mcp__serena"}, granted,
		"every attached server must be granted, companions included")
}

// TestPlanGrantIsServerLevel pins the grant to the SERVER form. A tool-suffixed
// rule (mcp__ctxloom__search_content) would grant that one tool and silently
// withhold every other read-only tool on the same server — and, worse, could
// never cover a companion whose tools ctxloom cannot enumerate.
func TestPlanGrantIsServerLevel(t *testing.T) {
	args := permissionArgs(agent.PermissionPlan, []string{"ctxloom"})
	for _, g := range grantedTools(t, args) {
		assert.Equal(t, agent.MCPToolPrefix+"ctxloom", g)
		assert.NotContains(t, strings.TrimPrefix(g, agent.MCPToolPrefix), "__",
			"%q is tool-scoped; the grant must name the server alone", g)
	}
}

// TestPlanWithNoServersEmitsNoGrantFlag guards the argv hazard buildArgs
// documents: --allowedTools is VARIADIC in claude's parser, so emitting it
// with an empty value next to a positional lets it swallow the prompt.
func TestPlanWithNoServersEmitsNoGrantFlag(t *testing.T) {
	for name, servers := range map[string][]string{
		"nil":         nil,
		"empty slice": {},
		"blank name":  {""},
	} {
		args := permissionArgs(agent.PermissionPlan, servers)
		assert.Equal(t, -1, indexOf(args, flagAllowedTools),
			"%s servers must emit no %s at all", name, flagAllowedTools)
	}
}

// TestPlanStillDeniesMutatingBuiltins guards the grant against widening the
// posture it rides with. plan is a read-only tier: an allowlist must not
// resurrect the write/exec tools the deny list exists to remove.
func TestPlanStillDeniesMutatingBuiltins(t *testing.T) {
	args := permissionArgs(agent.PermissionPlan, []string{"ctxloom"})

	i := indexOf(args, flagDisallowedTools)
	require.GreaterOrEqual(t, i, 0, "plan must still emit %s", flagDisallowedTools)
	for _, tool := range []string{"Bash", "Edit", "Write", "NotebookEdit"} {
		assert.Contains(t, args[i+1], tool, "plan must keep denying %s", tool)
		if j := indexOf(args, flagAllowedTools); j >= 0 {
			assert.NotContains(t, args[j+1], tool, "the grant must not re-admit %s", tool)
		}
	}
}

// TestNonPlanModesEmitNoGrant pins the grant to the one posture with a
// read-only tier behind it. A server-level grant under bypass or acceptEdits
// would be blanket permission for that server's MUTATING tools, because
// nothing else filters them there.
func TestNonPlanModesEmitNoGrant(t *testing.T) {
	for _, mode := range []agent.PermissionMode{
		agent.PermissionBypass, agent.PermissionAcceptEdits, agent.PermissionDefault,
	} {
		args := permissionArgs(mode, []string{"ctxloom", "taskloom"})
		assert.Equal(t, -1, indexOf(args, flagAllowedTools),
			"%s must not emit %s: it has no readOnlyHint gate to filter the server", mode, flagAllowedTools)
	}
}


func grantedTools(t *testing.T, args []string) []string {
	t.Helper()
	i := indexOf(args, flagAllowedTools)
	require.GreaterOrEqual(t, i, 0, "expected %s in %v", flagAllowedTools, args)
	require.Less(t, i+1, len(args), "%s emitted with no value token", flagAllowedTools)
	return strings.Split(args[i+1], ",")
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
