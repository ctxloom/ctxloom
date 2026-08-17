package claude

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestChatACPConfig_StripsNestedSessionGuard is the regression pin for a real
// incident: a delegated claude child's engine chain is spawned from inside
// the parent claude's process tree, inherits CLAUDECODE,
// and claude's nested-session guard then refuses to start — the child died at
// session/new with an opaque -32603 before its first turn. The chat driver
// must strip the guard variable for its deliberate, independent engine spawn.
func TestChatACPConfig_StripsNestedSessionGuard(t *testing.T) {
	cfg := chatACPConfig(map[string]string{"FOO": "bar"}, ClaudeACPAdapter)
	assert.Equal(t, ClaudeACPAdapter, cfg.Command)
	assert.Equal(t, map[string]string{"FOO": "bar"}, cfg.Env, "backend env overlay passes through")
	assert.Contains(t, cfg.StripEnv, "CLAUDECODE", "the nested-session guard must not leak into the child engine")
}

// TestResolveModel_TranslatesDocumentedNicknames pins the alias table: every
// documented claude interactive nickname (settable via the TUI's `/model`
// picker) resolves to its concrete, ACP/API-shaped id. Matching is
// case-insensitive since the saved interactive default's casing is not
// something ctxloom controls.
func TestResolveModel_TranslatesDocumentedNicknames(t *testing.T) {
	cases := map[string]string{
		"fable":  "claude-fable-5",
		"opus":   "claude-opus-4-8",
		"sonnet": "claude-sonnet-5",
		"haiku":  "claude-haiku-4-5",
		"Fable":  "claude-fable-5",
		"SONNET": "claude-sonnet-5",
	}
	for raw, want := range cases {
		model, ok := ResolveModel(raw)
		assert.True(t, ok, "nickname %q should resolve", raw)
		assert.Equal(t, want, model, "nickname %q", raw)
	}
}

// TestResolveModel_ConcretePassesThroughUntouched pins that an already-pinned
// concrete model id is never rewritten — only the documented bare nicknames
// are translated.
func TestResolveModel_ConcretePassesThroughUntouched(t *testing.T) {
	for _, raw := range []string{"claude-sonnet-5", "claude-opus-4-8", "claude-haiku-4-5-20251001"} {
		model, ok := ResolveModel(raw)
		assert.True(t, ok, "concrete id %q should resolve", raw)
		assert.Equal(t, raw, model, "a pinned concrete model must pass through untouched")
	}
}

// TestResolveModel_EmptyOrUnknownShapedFails pins the fail-loud shape: an
// empty model (never configured) and an unrecognized bare word (an
// interactive-only alias ctxloom has no translation for) both report
// unresolved rather than silently reaching the ACP/API path.
func TestResolveModel_EmptyOrUnknownShapedFails(t *testing.T) {
	for _, raw := range []string{"", "some-custom-alias", "latest", "default"} {
		model, ok := ResolveModel(raw)
		assert.False(t, ok, "raw %q must not resolve", raw)
		assert.Empty(t, model)
	}
}

// TestChatACPConfig_ModelEnvVar pins the ANTHROPIC_MODEL delivery config:
// claude's adapter entry names the claude SDK's own model-selector env var,
// because claude-code-acp 0.16.2 silently ignores the driver's --model argv
// and would otherwise run every session on the user's saved interactive
// default — re-opening the -32603 the resolveChatModel gate closed. The
// driver-side delivery is pinned in internal/acp (TestChat_ModelEnvVar).
func TestChatACPConfig_ModelEnvVar(t *testing.T) {
	assert.Equal(t, "ANTHROPIC_MODEL", chatACPConfig(nil, ClaudeACPAdapter).ModelEnvVar)
}

// TestClaudeThinkingTokens pins the enum -> MAX_THINKING_TOKENS mapping:
// off sets nothing at all (ok=false, the verified zero-thinking baseline);
// medium is 10000, the value VERIFIED live to actually produce thought
// chunks (29, vs 0 with the var absent).
func TestClaudeThinkingTokens(t *testing.T) {
	cases := []struct {
		level   agent.ThinkingLevel
		want    string
		wantSet bool
	}{
		{agent.ThinkingOff, "", false},
		{agent.ThinkingLow, "4000", true},
		{agent.ThinkingMedium, "10000", true},
		{agent.ThinkingHigh, "24000", true},
	}
	for _, tc := range cases {
		got, ok := claudeThinkingTokens(tc.level)
		assert.Equal(t, tc.wantSet, ok, "level %v", tc.level)
		assert.Equal(t, tc.want, got, "level %v", tc.level)
	}
}

// TestWithThinkingEnv_MediumSetsVar pins the default path: a medium level
// (ctxloom's documented default) adds MAX_THINKING_TOKENS=10000 without
// touching any other key, and never mutates the caller's map.
func TestWithThinkingEnv_MediumSetsVar(t *testing.T) {
	callerEnv := map[string]string{"FOO": "bar"}
	got := withThinkingEnv(callerEnv, agent.ThinkingMedium)
	assert.Equal(t, "10000", got["MAX_THINKING_TOKENS"])
	assert.Equal(t, "bar", got["FOO"])
	assert.NotContains(t, callerEnv, "MAX_THINKING_TOKENS", "the caller's map is copied, never mutated")
}

// TestWithThinkingEnv_OffSetsNoVar pins that an explicit off adds no env
// var at all, matching the verified absent-var baseline (0 thought chunks)
// rather than a value claude might interpret as a nonzero budget.
func TestWithThinkingEnv_OffSetsNoVar(t *testing.T) {
	callerEnv := map[string]string{"FOO": "bar"}
	got := withThinkingEnv(callerEnv, agent.ThinkingOff)
	assert.NotContains(t, got, "MAX_THINKING_TOKENS")
	assert.Equal(t, callerEnv, got)
}

// TestWithThinkingEnv_CallerOverrideWins pins that a caller-supplied
// MAX_THINKING_TOKENS already in env is never clobbered by the resolved
// default.
func TestWithThinkingEnv_CallerOverrideWins(t *testing.T) {
	callerEnv := map[string]string{"MAX_THINKING_TOKENS": "99"}
	got := withThinkingEnv(callerEnv, agent.ThinkingMedium)
	assert.Equal(t, "99", got["MAX_THINKING_TOKENS"])
}

// TestChat_ACPTransportGate_AdapterMissing pins that Chat() gates on the
// backend's OWN injected agent.ACPTransport (SetACPTransport — the same seam
// internal/lm/backends' registry uses at construction), not a hardcoded
// ClaudeACPAdapter reference: an adapter absent from PATH fails loud, naming
// the declaration's Binary and InstallCmd, and closes `out` exactly once
// per the StructuredChat contract.
func TestChat_ACPTransportGate_AdapterMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // guaranteed empty
	b := NewClaudeCode()
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
	b := NewClaudeCode()
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

// TestClaudeModelSelectionQuirk_PinnedDeclaration pins the quirk a past review
// wanted gone. That review was right that session/set_model is unstable and
// undocumented and that the coupling is version-scoped — that is what it IS:
// the sanctioned quarantine for a LIVE, controlled-experiment-verified defect
// where claude-code-acp 0.16.2 ignores argv --model, ignores ANTHROPIC_MODEL,
// does not implement session/set_config_option at all, and calls
// query.setModel from its own list on every session/new. Removing the quirk
// restores the bug (every claude ACP session silently running the wrong
// model); it cannot be routed through a spec channel the adapter does not
// honor. So that review is refuted and the workaround is pinned instead —
// nothing pinned it before, in either package.
//
// Each field is load-bearing at a distance: internal/acp's applyModelQuirk fires
// ONLY when the connected agent's self-reported initialize identity matches
// AgentName and one of AdapterVersions EXACTLY, and calls Method by name. Any
// drift here silently returns to the broken path, so a change to this
// declaration must be a deliberate edit with the removal condition (grep the
// adapter's dist/*.js for set_config_option) re-checked — see the doc comment.
func TestClaudeModelSelectionQuirk_PinnedDeclaration(t *testing.T) {
	require.NotNil(t, claudeModelSelectionQuirk, "the quirk is the only working model-selection channel for this adapter")
	assert.Equal(t, "session/set_model", claudeModelSelectionQuirk.Method,
		"the wire method name comes from the adapter's SDK schema, not its JS handler identifier")
	assert.Equal(t, "@zed-industries/claude-code-acp", claudeModelSelectionQuirk.AgentName,
		"must match the adapter's self-reported initialize agentInfo.name")
	assert.Equal(t, []string{"0.16.2"}, claudeModelSelectionQuirk.AdapterVersions,
		"exact-version scoping is the removal mechanism: widening this list must be a deliberate act, "+
			"not a side effect of an unrelated edit")
}
