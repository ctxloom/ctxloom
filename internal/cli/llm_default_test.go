// Plugin discovery tests verify that ctxloom correctly identifies built-in LM plugins
// (claude-code, antigravity, codex) and any user-configured plugins. This is essential
// for the `ctxloom run` command to know which backends are available for context injection.
package cli

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
)

// =============================================================================
// Plugin Recognition Tests
// =============================================================================
// ctxloom must recognize built-in plugins without explicit configuration,
// while rejecting unknown plugin names to prevent typos.

func TestIsKnownLLM_BuiltIn(t *testing.T) {
	// Built-in plugins must be recognized even with empty config
	cfg := &config.Config{}
	if !isKnownLLM(cfg, "claude-code") {
		t.Error("expected claude-code to be known")
	}
	if !isKnownLLM(cfg, "antigravity") {
		t.Error("expected antigravity to be known")
	}
}

func TestIsKnownLLM_Unknown(t *testing.T) {
	// Unknown plugins should be rejected to catch typos early
	cfg := &config.Config{}
	if isKnownLLM(cfg, "nonexistent-plugin") {
		t.Error("expected nonexistent-plugin to be unknown")
	}
}

// Plugin listing tests (operations.AvailableLLMNames) moved to
// internal/operations/llm_test.go alongside the function (ISO0 extraction).
