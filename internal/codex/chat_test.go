package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChatACPConfig_ModelConfigKey pins the Wave C3 finding: codex-acp
// 0.16.0 has NO --model flag (verified live — it exits 2, "unexpected
// argument '--model' found", a hard spawn failure), so codex's structured
// chat delivers the model through codex-acp's own `-c key=value` config
// override instead. The driver-side rendering (`-c model="<value>"`) is
// pinned in internal/acp (TestChatArgv_ModelConfigKey).
func TestChatACPConfig_ModelConfigKey(t *testing.T) {
	cfg := chatACPConfig(map[string]string{"FOO": "bar"})
	assert.Equal(t, codexACPAdapter, cfg.Command)
	assert.Equal(t, map[string]string{"FOO": "bar"}, cfg.Env, "backend env overlay passes through")
	assert.Equal(t, "model", cfg.ModelConfigKey)
	assert.Empty(t, cfg.Agent, "codex-acp rejects --agent too (verified live); never set it")
	assert.Empty(t, cfg.AgentEngine, "codex-acp rejects --agent-engine too (verified live); never set it")
	assert.Empty(t, cfg.ModelEnvVar, "codex has no known model env var; the config-override flag is the delivery")
	assert.Empty(t, cfg.StripEnv, "codex has no nested-session guard, unlike claude")
}
