package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
		ForwardPermissions: false,
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

// TestBuildHarnessSpec_RefusesNonHeadlessSafePermission: D3 is a property of
// the SPEC, so the encoding end enforces it too. Leaving the check to the
// decoding end alone means the coordinator happily composes a spec the runner
// is obliged to refuse, and the refusal only surfaces after a StartRun
// round-trip -- as an opaque runner-side rejection instead of a coordinator
// error naming the posture it built.
func TestBuildHarnessSpec_RefusesNonHeadlessSafePermission(t *testing.T) {
	for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionAcceptEdits} {
		_, err := buildHarnessSpec(HarnessSpecInput{
			Harness:    "claude-code",
			Model:      "claude-sonnet-5",
			Permission: perm,
		})
		require.Error(t, err, "permission mode %q must be refused at the encode end", perm)
		assert.Contains(t, err.Error(), "headless-safe")
	}
}

// TestHeadlessSafePermission_NeverYieldsAnUnsafeMode pins the gate that runs
// BEFORE any credential is minted or process spawned: agent resolution coerces
// (degraded) or refuses (strict) an unsafe declared posture, so plan.Perm --
// the only child-spawn input to buildHarnessSpec -- is headless-safe by the
// time a spawn is even enqueued.
func TestHeadlessSafePermission_NeverYieldsAnUnsafeMode(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)

	for _, declared := range []string{"", "default", "acceptEdits", "accept-edits", "not-a-mode"} {
		mode, degraded := headlessSafePermission("worker", declared)
		assert.True(t, mode.SafeHeadless(), "declared %q resolved to non-headless-safe %q", declared, mode)
		assert.NotEmpty(t, degraded, "the coercion must be reported to the operator")
	}

	mode, degraded := headlessSafePermission("worker", "bypass")
	assert.Equal(t, agent.PermissionBypass, mode, "an already-safe posture passes through untouched")
	assert.Empty(t, degraded)
}

// TestDecodeHarnessSpec_RefusesMalformedMCPServers: a config entry that is not
// a usable {name, command} pair was coerced into an empty ChatMCPServer, which
// the engine would then try to launch as a server with no name and no
// executable. An unusable entry must be refused where it is decoded.
func TestDecodeHarnessSpec_RefusesMalformedMCPServers(t *testing.T) {
	cases := map[string]any{
		"entry is not an object":   "tools",
		"entry has no command":     map[string]any{"name": "tools"},
		"entry has no name":        map[string]any{"command": "/bin/tools"},
		"entry is an empty object": map[string]any{},
	}
	for name, entry := range cases {
		cfg, err := structpb.NewStruct(map[string]any{harnessConfigKeyMCPServers: []any{entry}})
		require.NoError(t, err)
		_, err = decodeHarnessSpec(&agentcoordpb.HarnessSpec{
			Harness: "claude-code", PermissionMode: "bypass", Config: cfg,
		})
		require.Error(t, err, "%s must be refused", name)
		assert.Contains(t, err.Error(), harnessConfigKeyMCPServers, "the error must name the offending config key: %s", name)
	}
}

// TestDecodeHarnessSpec_KeepsWellFormedMCPServers: the refusal above is narrow
// -- a complete entry still decodes, including one with no args and no env.
func TestDecodeHarnessSpec_KeepsWellFormedMCPServers(t *testing.T) {
	cfg, err := structpb.NewStruct(map[string]any{harnessConfigKeyMCPServers: []any{
		map[string]any{"name": "tools", "command": "/bin/tools"},
	}})
	require.NoError(t, err)
	out, err := decodeHarnessSpec(&agentcoordpb.HarnessSpec{
		Harness: "claude-code", PermissionMode: "bypass", Config: cfg,
	})
	require.NoError(t, err)
	assert.Equal(t, []agent.ChatMCPServer{{Name: "tools", Command: "/bin/tools"}}, out.Chat.MCPServers)
}
