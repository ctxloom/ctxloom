package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestHarnessSpec_RoundTrip pins the C0 wire convention end to end:
// buildHarnessSpec's output decodes back through decodeHarnessSpec into an
// equivalent agent.ChatRequest + session harp, so the two ends of StartRun
// (coordinator encode, runner decode) cannot drift apart.
func TestHarnessSpec_RoundTrip(t *testing.T) {
	in := HarnessSpecInput{
		Harness:   "claude-code",
		Model:     "claude-sonnet-5",
		Workspace: "/work/child-1",
		ExtraArgs: []string{"--verbose"},
		Env:       map[string]string{"CTXLOOM_PROJECT_ID": "proj-1"},
		MCPServers: []agent.ChatMCPServer{
			{Name: "tools", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}},
		},
		SessionHarp:     "child-harp-9",
		Permission:      agent.PermissionBypass,
		ResumeSessionID: "native-sess-1",
	}
	spec, err := buildHarnessSpec(in)
	require.NoError(t, err)
	assert.Equal(t, "claude-code", spec.Harness)
	assert.Equal(t, "claude-sonnet-5", spec.Model)
	assert.Equal(t, "/work/child-1", spec.Workspace)
	assert.Equal(t, []string{"--verbose"}, spec.ExtraArgs)
	assert.Equal(t, "bypass", spec.PermissionMode, "the typed field carries the wire spelling")
	assert.Equal(t, "native-sess-1", spec.ResumeSessionId)

	out, err := decodeHarnessSpec(spec)
	require.NoError(t, err)
	assert.Equal(t, "child-harp-9", out.SessionHarp)
	assert.Equal(t, agent.ChatRequest{
		WorkDir:     "/work/child-1",
		Model:       "claude-sonnet-5",
		Env:         map[string]string{"CTXLOOM_PROJECT_ID": "proj-1"},
		Permissions: agent.PermissionBypass,
		// C2: the migrated path always forwards permissions now — the
		// coordinator's escalation ladder is the decider, not the driver.
		ForwardPermissions: true,
		MCPServers:         []agent.ChatMCPServer{{Name: "tools", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}}},
		ResumeSessionID:    "native-sess-1",
	}, out.Chat)
}

// TestHarnessSpec_MinimalRoundTrip: no env, no MCP servers, no resume, no
// session harp -- the empty-Struct-config path (buildHarnessSpec must not
// crash or emit a bogus empty Struct that decodes into non-nil maps).
func TestHarnessSpec_MinimalRoundTrip(t *testing.T) {
	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness:    "claude-code",
		Model:      "claude-sonnet-5",
		Permission: agent.PermissionPlan,
	})
	require.NoError(t, err)
	assert.Nil(t, spec.Config)

	out, err := decodeHarnessSpec(spec)
	require.NoError(t, err)
	assert.Empty(t, out.SessionHarp)
	assert.Nil(t, out.Chat.Env)
	assert.Nil(t, out.Chat.MCPServers)
	assert.Equal(t, agent.PermissionPlan, out.Chat.Permissions)
}

// TestDecodeHarnessSpec_RefusesNonHeadlessSafePermission pins D3: an unset,
// unparseable, or non-headless-safe permission_mode is refused rather than
// silently defaulted -- the runner must honor EXACTLY what the coordinator
// declared.
func TestDecodeHarnessSpec_RefusesNonHeadlessSafePermission(t *testing.T) {
	cases := []string{"", "bogus", "default", "acceptEdits"}
	for _, pm := range cases {
		spec := &agentcoordpb.HarnessSpec{Harness: "claude-code", Model: "claude-sonnet-5", PermissionMode: pm}
		_, err := decodeHarnessSpec(spec)
		require.Error(t, err, "permission_mode %q must be refused", pm)
	}
}

// TestDecodeHarnessSpec_NilSpec refuses a nil HarnessSpec outright.
func TestDecodeHarnessSpec_NilSpec(t *testing.T) {
	_, err := decodeHarnessSpec(nil)
	require.Error(t, err)
}
