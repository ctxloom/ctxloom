package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestGetToolReflectBytes covers all three branches of the switch that decides
// whether the PostToolUse reflect hook exists and at what size it fires.
//
// The disable branch is why this returns (int, bool) rather than signalling
// "off" with a zero: zero already means "unset, use the default", so a caller
// inferring disabled-ness from the value would silently enable the hook for
// anyone who tried to turn it off.
func TestGetToolReflectBytes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		set           int
		wantThreshold int
		wantEnabled   bool
	}{
		{"unset uses the shared default", 0, agent.DefaultToolReflectBytes, true},
		{"explicit value is honoured", 512, 512, true},
		{"negative disables the hook", -1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{settings: SettingsConfig{ToolReflectBytes: tc.set}}

			threshold, enabled := cfg.GetToolReflectBytes()

			if enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", enabled, tc.wantEnabled)
			}
			if threshold != tc.wantThreshold {
				t.Fatalf("threshold = %d, want %d", threshold, tc.wantThreshold)
			}
		})
	}
}

// TestSettingsHasAnyCoversToolReflectBytes pins the invariant hasAny's own doc
// comment states: it MUST cover every field, because a setting it does not
// mention is silently dropped from the file on the next Save. Setting only
// this field must keep the block alive.
func TestSettingsHasAnyCoversToolReflectBytes(t *testing.T) {
	if (SettingsConfig{}).hasAny() {
		t.Fatal("an empty SettingsConfig reports having settings")
	}
	if !(SettingsConfig{ToolReflectBytes: 4096}).hasAny() {
		t.Fatal("tool_reflect_bytes alone does not keep the settings block; Save would drop it")
	}
	if !(SettingsConfig{ToolReflectBytes: -1}).hasAny() {
		t.Fatal("a DISABLING tool_reflect_bytes does not survive Save, so the hook silently comes back")
	}
}

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
// the new field: a config that sets ONLY the essence budget must survive Save.
func TestSettingsHasAnyCoversEssenceMaxChars(t *testing.T) {
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
