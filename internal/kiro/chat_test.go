package kiro

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChatACPConfig_UsesGenericModelFlag pins the Wave C3 finding: unlike
// codex-acp, `kiro-cli acp --model <id>` is a real, CLI-parse-accepted flag
// (verified live: `kiro-cli --help-all` documents it, and an unauthenticated
// live spawn with an arbitrary --model value fails on kiro-cli's own auth
// check rather than on argument parsing) — so kiro rides the driver's
// generic --model argv untouched, exactly like buildArgs already does for
// the oneshot `kiro-cli chat` path (backend.go). No ModelConfigKey/
// ModelEnvVar override is wired.
func TestChatACPConfig_UsesGenericModelFlag(t *testing.T) {
	b := NewKiro()
	cfg := b.chatACPConfig()
	assert.Equal(t, "kiro-cli acp", cfg.Command)
	assert.Equal(t, defaultAgentName, cfg.Agent)
	assert.Empty(t, cfg.ModelConfigKey, "kiro's --model flag works; no override needed")
	assert.Empty(t, cfg.ModelEnvVar, "kiro has no known model env var precedent; --model argv is the mechanism")
}

// TestChatACPConfig_CarriesConfiguredKnobs pins that agent/agent-engine/args/
// env configured onto the backend reach the embedded ACP driver unchanged.
func TestChatACPConfig_CarriesConfiguredKnobs(t *testing.T) {
	b := NewKiro()
	b.Configure(&KiroConfig{Agent: "custom-agent", AgentEngine: "v3", Env: map[string]string{"FOO": "bar"}})
	cfg := b.chatACPConfig()
	assert.Equal(t, "custom-agent", cfg.Agent)
	assert.Equal(t, "v3", cfg.AgentEngine)
	assert.Equal(t, map[string]string{"FOO": "bar"}, cfg.Env)
}
