package operations

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// env_host: false and its env names round-trip through the config, and the
// written entry and the declared view both carry them.
func TestSetAgent_PersistsEnvHostAndEnv(t *testing.T) {
	cfg, appDir := loadConfigDir(t, fmt.Sprintf("schema_version: %d\n", config.CurrentConfigVersion))
	entry, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{
		Name: "coder", LLM: ptr("claude-code"), EnvHost: ptr(false), Env: ptr([]string{"GITHUB_TOKEN"}),
	})
	require.NoError(t, err)
	require.NotNil(t, entry.EnvHost)
	assert.False(t, *entry.EnvHost)
	assert.Equal(t, []string{"GITHUB_TOKEN"}, entry.Env)

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Agent("coder")
	require.True(t, ok)
	assert.Equal(t, agents.EnvHost{Curated: true, Env: []string{"GITHUB_TOKEN"}}, sub.HostEnv())
	got, err := GetAgent(reloaded, "coder")
	require.NoError(t, err)
	require.NotNil(t, got.EnvHost)
	assert.False(t, *got.EnvHost)
	assert.Equal(t, []string{"GITHUB_TOKEN"}, got.Env)
	assert.Equal(t, []string{"GITHUB_TOKEN"}, ListAgents(reloaded)[0].Env)

	// An explicitly empty env clears the names, leaving env_host as it was.
	_, err = SetAgent(context.Background(), managerFor(t, appDir), reloaded, SetAgentRequest{Name: "coder", Env: ptr([]string{})})
	require.NoError(t, err)
	cleared, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	sub, _ = cleared.Agent("coder")
	assert.Equal(t, agents.EnvHost{Curated: true}, sub.HostEnv())
}

// A write whose RESULTING record would not do what it says is refused and
// persists nothing: env with env_host left on (absent, or flipped back on
// over recorded names), and an entry that is not a bare name.
func TestSetAgent_RefusesAnEnvTheBindingWouldIgnore(t *testing.T) {
	cfg, appDir := loadConfigDir(t, fmt.Sprintf(`schema_version: %d
agents:
  curated:
    llm: claude-code
    env_host: false
    env: [GITHUB_TOKEN]
`, config.CurrentConfigVersion))
	for name, c := range map[string]struct {
		req  SetAgentRequest
		want error
	}{
		"env without env_host false": {SetAgentRequest{Name: "loose", LLM: ptr("claude-code"), Env: ptr([]string{"GITHUB_TOKEN"})}, agents.ErrEnvWithEnvHost},
		"env_host flipped back on":   {SetAgentRequest{Name: "curated", EnvHost: ptr(true)}, agents.ErrEnvWithEnvHost},
		"NAME=value":                 {SetAgentRequest{Name: "valued", LLM: ptr("claude-code"), EnvHost: ptr(false), Env: ptr([]string{"GITHUB_TOKEN=abc"})}, agents.ErrEnvNotBareName},
	} {
		_, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, c.req)
		require.ErrorIs(t, err, c.want, name)
		assert.Contains(t, err.Error(), c.req.Name, name)
	}
	final, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := final.Agent("loose")
	assert.False(t, ok, "a refused create persists nothing")
	sub, _ := final.Agent("curated")
	assert.Equal(t, agents.EnvHost{Curated: true, Env: []string{"GITHUB_TOKEN"}}, sub.HostEnv(), "a refused edit leaves the binding as it was")
}
