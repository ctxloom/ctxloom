package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestChatACPConfig_ModelConfigKey pins the Wave C3 finding: codex-acp
// 0.16.0 has NO --model flag (verified live — it exits 2, "unexpected
// argument '--model' found", a hard spawn failure), so codex's structured
// chat delivers the model through codex-acp's own `-c key=value` config
// override instead. The driver-side rendering (`-c model="<value>"`) is
// pinned in internal/acp (TestChatArgv_ModelConfigKey).
func TestChatACPConfig_ModelConfigKey(t *testing.T) {
	cfg := chatACPConfig(map[string]string{"FOO": "bar"}, agent.ThinkingMedium)
	assert.Equal(t, codexACPAdapter, cfg.Command)
	assert.Equal(t, map[string]string{"FOO": "bar"}, cfg.Env, "backend env overlay passes through")
	assert.Equal(t, "model", cfg.ModelConfigKey)
	assert.Empty(t, cfg.Agent, "codex-acp rejects --agent too (verified live); never set it")
	assert.Empty(t, cfg.AgentEngine, "codex-acp rejects --agent-engine too (verified live); never set it")
	assert.Empty(t, cfg.ModelEnvVar, "codex has no known model env var; the config-override flag is the delivery")
	assert.Empty(t, cfg.StripEnv, "codex has no nested-session guard, unlike claude")
}

// TestChatACPConfig_ReasoningEffort pins the enum -> codex-acp translation:
// VERIFIED real model_reasoning_effort values are minimal/low/medium/xhigh
// — there is NO bare "high" — so the normalized "high" must map to "xhigh",
// never a "high" codex's own config validation would reject. Every non-off
// level also carries a model_reasoning_summary of "auto".
func TestChatACPConfig_ReasoningEffort(t *testing.T) {
	cases := []struct {
		level       agent.ThinkingLevel
		wantEffort  string
		wantSummary string
	}{
		{agent.ThinkingOff, "minimal", ""},
		{agent.ThinkingLow, "low", "auto"},
		{agent.ThinkingMedium, "medium", "auto"},
		{agent.ThinkingHigh, "xhigh", "auto"},
	}
	for _, tc := range cases {
		cfg := chatACPConfig(nil, tc.level)
		assert.Equal(t, "model_reasoning_effort", cfg.ReasoningConfigKey, "level %v", tc.level)
		assert.Equal(t, tc.wantEffort, cfg.ReasoningEffort, "level %v", tc.level)
		if tc.wantSummary == "" {
			assert.Empty(t, cfg.ReasoningSummaryConfigKey, "level %v: off carries no summary override", tc.level)
			assert.Empty(t, cfg.ReasoningSummary, "level %v", tc.level)
		} else {
			assert.Equal(t, "model_reasoning_summary", cfg.ReasoningSummaryConfigKey, "level %v", tc.level)
			assert.Equal(t, tc.wantSummary, cfg.ReasoningSummary, "level %v", tc.level)
		}
	}
}
