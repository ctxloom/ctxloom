package isolation

import (
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearClaudeAuthEnv unsets every var that can authenticate a claude
// container, so a test sets only the ones it names.
func clearClaudeAuthEnv(t *testing.T) {
	t.Helper()
	for _, v := range claudeAuth(t).EnvTriggers {
		t.Setenv(v, "")
	}
}

// TestPresentEnvKeys_OnlyKnownSetVars: the scoped auth-env set carries ONLY the
// NAMES of the known auth vars that are actually set — never a value (the value
// would leak into the world-readable `run` argv), and never the host's full
// environment.
func TestPresentEnvKeys_OnlyKnownSetVars(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "", "PATH": "/x"}
	out := presentEnvKeys(func(k string) string { return env[k] }, claudeAuth(t).EnvPassthrough)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, out, "only set, known auth var NAMES cross (no value; empty + unknown dropped)")
}

// The setup-token var alone authenticates a claude container: it selects env
// passthrough and crosses by NAME, so the value stays in the launcher's env
// and out of the world-readable run argv.
func TestResolveDeclaredAuth_SetupTokenAloneCrossesByName(t *testing.T) {
	clearClaudeAuthEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-secret")
	plan, ok := resolveDeclaredAuth(claudeAuth(t), engine.LaunchEnv{})
	require.True(t, ok, "a stored or exported setup-token authenticates the container")
	assert.Equal(t, authEnv, plan.mode)
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN"}, plan.envPassthrough)
	for _, e := range plan.envPassthrough {
		assert.NotContains(t, e, "=", "a value must never be carried in the passthrough")
	}
}

// With no auth var set, a claude container is refused (ok=false, the caller
// degrades and the choke owner aborts): no credential file is ever mounted
// in its place, however many sit in the host home.
func TestResolveDeclaredAuth_NoAuthVarRefusesAndMountsNothing(t *testing.T) {
	clearClaudeAuthEnv(t)
	auth, ok := resolveDeclaredAuth(claudeAuth(t), engine.LaunchEnv{})
	assert.False(t, ok)
	assert.Equal(t, authNone, auth.mode)

	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok = resolveDeclaredAuth(claudeAuth(t), engine.LaunchEnv{})
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_API_KEY", "the auth var crosses by NAME")
	for _, e := range auth.envPassthrough {
		assert.NotContains(t, e, "sk-test", "the secret VALUE must never be stored in the auth plan")
	}
}

// A gateway host authenticates via ANTHROPIC_BASE_URL+ANTHROPIC_AUTH_TOKEN
// and carries no ANTHROPIC_API_KEY at all; the token alone must trigger.
func TestResolveClaudeContainerAuth_AuthTokenAlsoTriggers(t *testing.T) {
	clearClaudeAuthEnv(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "gw-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example")

	auth, ok := resolveDeclaredAuth(claudeAuth(t), engine.LaunchEnv{})
	require.True(t, ok, "ANTHROPIC_AUTH_TOKEN alone must trigger env passthrough")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_AUTH_TOKEN")
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_BASE_URL")
	for _, e := range auth.envPassthrough {
		assert.NotContains(t, e, "gw-token", "the secret VALUE must never be stored in the auth plan")
	}
}

// The trigger is an auth var specifically, NOT any ANTHROPIC_* var: a base
// URL or model alone must not select passthrough, or a run launches against a
// partial, keyless env. Kills the mutant that triggers on
// len(presentEnvKeys) > 0.
func TestResolveClaudeContainerAuth_TriggersOnAuthVarsNotOtherAnthropicVars(t *testing.T) {
	clearClaudeAuthEnv(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://x")
	t.Setenv("ANTHROPIC_MODEL", "claude-x")

	auth, ok := resolveDeclaredAuth(claudeAuth(t), engine.LaunchEnv{})
	require.False(t, ok, "other ANTHROPIC_* set without an auth var must NOT env-trigger")
	assert.Equal(t, authNone, auth.mode)
	assert.Empty(t, auth.envPassthrough, "nothing crosses when no trigger var is set")
}

// TestContainerAuthMode_String documents the diagnostic labels (no secrets).
func TestContainerAuthMode_String(t *testing.T) {
	assert.Equal(t, "env-passthrough", authEnv.String())
	assert.Equal(t, "none", authNone.String())
}
