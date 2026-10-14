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

// TestSettingsHasAnyCoversEssenceMaxChars pins hasAny's stated invariant for
// this field: a config that sets ONLY the essence budget must survive Save,
// and an empty one reports nothing.
func TestSettingsHasAnyCoversEssenceMaxChars(t *testing.T) {
	if (SettingsConfig{}).hasAny() {
		t.Fatal("an empty SettingsConfig reports having settings")
	}
	if !(SettingsConfig{EssenceMaxChars: 20000}).hasAny() {
		t.Fatal("essence_max_chars alone does not keep the settings block; Save would drop it")
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
	if !(SettingsConfig{SilenceUnsupported: true}).hasAny() {
		t.Fatal("silence_unsupported alone does not survive Save, so the opt-out silently reverts")
	}
}
