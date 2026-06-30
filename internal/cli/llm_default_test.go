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

// =============================================================================
// Plugin Listing Tests
// =============================================================================
// Available plugin list is shown to users in help and error messages.

func TestAvailableLLMNames_IncludesBuiltIns(t *testing.T) {
	// All built-in plugins must appear in the available list
	cfg := &config.Config{}
	names := availableLLMNames(cfg)

	expected := map[string]bool{
		"claude-code": false,
		"antigravity": false,
		"codex":       false,
	}

	for _, name := range names {
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected %s in available plugin names", name)
		}
	}
}

func TestAvailableLLMNames_Sorted(t *testing.T) {
	// Sorted output provides consistent, scannable display to users
	cfg := &config.Config{}
	names := availableLLMNames(cfg)

	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("expected sorted names, but %q < %q at index %d", names[i], names[i-1], i)
		}
	}
}
