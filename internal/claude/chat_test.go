package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChatACPConfig_StripsNestedSessionGuard is the regression pin for the
// agentcoord Wave B1 child-death: a delegated claude child's engine chain is
// spawned from inside the parent claude's process tree, inherits CLAUDECODE,
// and claude's nested-session guard then refuses to start — the child died at
// session/new with an opaque -32603 before its first turn. The chat driver
// must strip the guard variable for its deliberate, independent engine spawn.
func TestChatACPConfig_StripsNestedSessionGuard(t *testing.T) {
	cfg := chatACPConfig(map[string]string{"FOO": "bar"})
	assert.Equal(t, claudeACPAdapter, cfg.Command)
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
	assert.Equal(t, "ANTHROPIC_MODEL", chatACPConfig(nil).ModelEnvVar)
}
