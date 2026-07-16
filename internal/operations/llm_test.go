package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
)

// =============================================================================
// Plugin Listing Tests
// =============================================================================
// Available plugin list is shown to users in help and error messages.
// Moved here from internal/cli (ISO0 extraction) alongside AvailableLLMNames.

func TestAvailableLLMNames_IncludesBuiltIns(t *testing.T) {
	// All built-in plugins must appear in the available list
	cfg := &config.Config{}
	names := AvailableLLMNames(cfg)

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
	names := AvailableLLMNames(cfg)

	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("expected sorted names, but %q < %q at index %d", names[i], names[i-1], i)
		}
	}
}
