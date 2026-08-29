//go:build parked_engines

package codex

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestChatACPConfig_ModelConfigKey pins the finding that codex-acp
// 0.16.0 has NO --model flag (verified live — it exits 2, "unexpected
// argument '--model' found", a hard spawn failure), so codex's structured
// chat delivers the model through codex-acp's own `-c key=value` config
// override instead. The driver-side rendering (`-c model="<value>"`) is
// pinned in internal/acp (TestChatArgv_ModelConfigKey).
func TestChatACPConfig_ModelConfigKey(t *testing.T) {
	cfg := chatACPConfig(map[string]string{"FOO": "bar"}, agent.ThinkingMedium, CodexACPAdapter)
	assert.Equal(t, CodexACPAdapter, cfg.Command)
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
		cfg := chatACPConfig(nil, tc.level, CodexACPAdapter)
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

// TestChat_ACPTransportGate_AdapterMissing pins that Chat() gates on the
// backend's OWN injected agent.ACPTransport (SetACPTransport — the same seam
// internal/lm/backends' registry uses at construction), not a hardcoded
// CodexACPAdapter reference: an adapter absent from PATH fails loud, naming
// the declaration's Binary and InstallCmd, and closes `out` exactly once
// per the StructuredChat contract.
func TestChat_ACPTransportGate_AdapterMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // guaranteed empty
	b := NewCodex()
	b.SetACPTransport(agent.ACPTransport{
		Kind:       agent.ACPAdapter,
		Binary:     "definitely-not-a-real-adapter-xyz",
		InstallCmd: "npm install -g @zed-industries/definitely-not-a-real-adapter-xyz",
	})
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	close(in)

	err := b.Chat(context.Background(), agent.ChatRequest{}, in, out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-not-a-real-adapter-xyz")
	assert.Contains(t, err.Error(), "npm install -g @zed-industries/definitely-not-a-real-adapter-xyz")

	select {
	case _, open := <-out:
		assert.False(t, open, "out must be closed by the producer on this error path")
	case <-time.After(time.Second):
		t.Fatal("out was never closed")
	}
}

// TestChat_ACPTransportGate_ContainerRuntimeExempt pins that a
// runtime:container chat is exempt from the host-PATH check even when the
// declared adapter binary genuinely resolves nowhere — the agent image is
// trusted to carry its own adapter, so Chat() must proceed past the gate
// (whatever error a real subprocess spawn eventually returns is NOT this
// "needs the adapter on PATH" error).
func TestChat_ACPTransportGate_ContainerRuntimeExempt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewCodex()
	b.SetACPTransport(agent.ACPTransport{
		Kind:       agent.ACPAdapter,
		Binary:     "definitely-not-a-real-adapter-xyz",
		InstallCmd: "npm install -g whatever",
	})
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	close(in)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- b.Chat(ctx, agent.ChatRequest{Runtime: agent.RuntimeContainerRootless}, in, out)
	}()
	go func() { //nolint:revive // drain so Chat never blocks on a send
		for range out {
		}
	}()

	select {
	case err := <-done:
		if err != nil {
			assert.NotContains(t, err.Error(), "adapter on PATH", "the container-runtime exemption must skip the host-PATH gate entirely")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chat did not return")
	}
}
