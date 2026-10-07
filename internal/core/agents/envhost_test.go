package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Undeclared is today's behaviour: the engine inherits every variable.
func TestEnvHost_UndeclaredInheritsEverything(t *testing.T) {
	assert.True(t, EnvHost{}.Inherits("UNRELATED_SECRET"))
}

// Curated keeps the base (exact names and prefixes) and the env names, and
// nothing else — an exact name is not a prefix.
func TestEnvHost_CuratedKeepsOnlyTheBaseAndTheEnvNames(t *testing.T) {
	h := EnvHost{Curated: true, Env: []string{"GITHUB_TOKEN"}}
	for _, k := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "COLORTERM", "LANG", "TMPDIR", "LC_ALL", "XDG_RUNTIME_DIR", "CTXLOOM_HARP", "GITHUB_TOKEN"} {
		assert.True(t, h.Inherits(k), "%s is kept", k)
	}
	for _, k := range []string{"UNRELATED_SECRET", "AWS_SECRET_ACCESS_KEY", "PATHX", "LC", "XDG", "GITHUB_TOKENX", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		assert.False(t, h.Inherits(k), "%s is dropped", k)
	}
}

// env names with env_host left on would be silently ignored, since every
// variable is inherited already; they are refused.
func TestEnvHost_EnvWithEnvHostOnIsRefused(t *testing.T) {
	require.ErrorIs(t, EnvHost{Env: []string{"X"}}.Validate(), ErrEnvWithEnvHost)
	require.NoError(t, EnvHost{Curated: true, Env: []string{"X"}}.Validate())
	require.NoError(t, EnvHost{}.Validate())
}

// env takes bare names only: a NAME=value entry would set a value, which
// this key does not do, and an empty entry names nothing.
func TestEnvHost_OnlyBareNamesAreAccepted(t *testing.T) {
	for _, bad := range []string{"GITHUB_TOKEN=abc", "=x", ""} {
		err := EnvHost{Curated: true, Env: []string{"OK", bad}}.Validate()
		require.ErrorIs(t, err, ErrEnvNotBareName, "%q", bad)
		assert.Contains(t, err.Error(), "only")
	}
}

// The binding's two keys resolve to the value: env_host absent or true
// inherits everything; false curates; env carries over either way, so that
// Validate can refuse it under env_host true.
func TestAgent_HostEnvResolvesTheTwoKeys(t *testing.T) {
	on, off := true, false
	assert.Equal(t, EnvHost{}, Agent{}.HostEnv())
	assert.Equal(t, EnvHost{Env: []string{"X"}}, Agent{EnvHost: &on, Env: []string{"X"}}.HostEnv())
	assert.Equal(t, EnvHost{Curated: true, Env: []string{"X"}}, Agent{EnvHost: &off, Env: []string{"X"}}.HostEnv())
}

// The keys are spelt as podman spells the flags: env_host and env. An
// explicit false survives a round trip (a plain bool would be dropped by
// omitempty and the opt-in would vanish).
func TestAgent_EnvHostKeysRoundTrip(t *testing.T) {
	var a Agent
	require.NoError(t, yaml.Unmarshal([]byte("env_host: false\nenv: [GITHUB_TOKEN]\n"), &a))
	require.NotNil(t, a.EnvHost)
	assert.False(t, *a.EnvHost)
	assert.Equal(t, []string{"GITHUB_TOKEN"}, a.Env)
	out, err := yamlx.Marshal(a)
	require.NoError(t, err)
	assert.Contains(t, string(out), "env_host: false")
	assert.Contains(t, string(out), "- GITHUB_TOKEN")
}
