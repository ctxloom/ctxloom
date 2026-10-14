package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// TestGetEssenceMaxChars covers both branches of the essence budget: an
// explicit value wins, and anything else takes the shared default.
func TestGetEssenceMaxChars(t *testing.T) {
	if got := (&Config{}).GetEssenceMaxChars(); got != agent.DefaultEssenceChars {
		t.Fatalf("unset budget = %d, want the shared default %d", got, agent.DefaultEssenceChars)
	}
	if got := (&Config{settings: SettingsConfig{EssenceMaxChars: 4321}}).GetEssenceMaxChars(); got != 4321 {
		t.Fatalf("explicit budget = %d, want 4321", got)
	}
}

// TestShouldSilenceUnsupported pins the default and the opt-in. The default
// matters most: a loss that goes quiet without anyone asking is the silent
// degradation this project treats as its characteristic bug.
func TestShouldSilenceUnsupported(t *testing.T) {
	if (&Config{}).ShouldSilenceUnsupported() {
		t.Fatal("capability loss is silenced by default; it must be audible unless opted out")
	}
	if !(&Config{settings: SettingsConfig{SilenceUnsupported: true}}).ShouldSilenceUnsupported() {
		t.Fatal("the opt-out does not take effect")
	}
}
