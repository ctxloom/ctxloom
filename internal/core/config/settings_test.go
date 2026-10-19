package config

import (
	"testing"
)

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
