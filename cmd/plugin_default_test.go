// Plugin discovery tests verify that SCM correctly identifies built-in LM plugins
// (claude-code, gemini, aider) and any user-configured plugins. This is essential
// for the `scm run` command to know which backends are available for context injection.
package cmd

import (
	"testing"

	"github.com/SophisticatedContextManager/scm/internal/config"
)

// =============================================================================
// Plugin Recognition Tests
// =============================================================================
// SCM must recognize built-in plugins without explicit configuration,
// while rejecting unknown plugin names to prevent typos.

func TestIsKnownPlugin_BuiltIn(t *testing.T) {
	// Built-in plugins must be recognized even with empty config
	cfg := &config.Config{}
	if !isKnownPlugin(cfg, "claude-code") {
		t.Error("expected claude-code to be known")
	}
	if !isKnownPlugin(cfg, "gemini") {
		t.Error("expected gemini to be known")
	}
}

func TestIsKnownPlugin_Unknown(t *testing.T) {
	// Unknown plugins should be rejected to catch typos early
	cfg := &config.Config{}
	if isKnownPlugin(cfg, "nonexistent-plugin") {
		t.Error("expected nonexistent-plugin to be unknown")
	}
}

// =============================================================================
// Plugin Listing Tests
// =============================================================================
// Available plugin list is shown to users in help and error messages.

func TestAvailablePluginNames_IncludesBuiltIns(t *testing.T) {
	// All built-in plugins must appear in the available list
	cfg := &config.Config{}
	names := availablePluginNames(cfg)

	expected := map[string]bool{
		"claude-code": false,
		"gemini":      false,
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

func TestAvailablePluginNames_Sorted(t *testing.T) {
	// Sorted output provides consistent, scannable display to users
	cfg := &config.Config{}
	names := availablePluginNames(cfg)

	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("expected sorted names, but %q < %q at index %d", names[i], names[i-1], i)
		}
	}
}
