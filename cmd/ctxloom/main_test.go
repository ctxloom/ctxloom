package main

import (
	"strings"
	"testing"
)

// A switch set to something no boolean spelling covers must be REPORTED. These
// are read before any flag is parsed, so an ignored value produces no other
// symptom than the mode not engaging — which reads as a broken feature.
func TestEnvSwitchOn_UnrecognizedValueIsReported(t *testing.T) {
	t.Setenv("CTXLOOM_MAIN_PIN_SWITCH", "yep")
	var warn strings.Builder
	if envSwitchOn("CTXLOOM_MAIN_PIN_SWITCH", &warn) {
		t.Error("an unparseable value turned the switch on")
	}
	if !strings.Contains(warn.String(), "CTXLOOM_MAIN_PIN_SWITCH") || !strings.Contains(warn.String(), "yep") {
		t.Errorf("the ignored value was not reported; warn stream = %q", warn.String())
	}
}

// The spellings an operator reaches for turn the switch on, and say nothing.
func TestEnvSwitchOn_RecognizedValuesAreSilent(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "true": true, "on": true, "0": false, "false": false, "": false} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("CTXLOOM_MAIN_PIN_SWITCH", value)
			var warn strings.Builder
			if got := envSwitchOn("CTXLOOM_MAIN_PIN_SWITCH", &warn); got != want {
				t.Errorf("envSwitchOn(%q) = %v, want %v", value, got, want)
			}
			if warn.String() != "" {
				t.Errorf("recognized value %q was reported: %q", value, warn.String())
			}
		})
	}
}
