package envswitch

import "testing"

const varName = "CTXLOOM_ENVSWITCH_PIN"

// The spellings an operator actually reaches for must all turn the switch on.
// Matching a single literal is what made `CTXLOOM_VERBOSE=true` a silent
// no-op: the behaviour is absent, nothing is reported, and there is no way to
// tell the switch from a broken feature.
func TestOn_AcceptedOnSpellings(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "True", "t", "yes", "YES", "y", "on", "ON", " true ", "\ttrue\n"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(varName, v)
			on, unrecognized := On(varName)
			if !on {
				t.Errorf("On(%q) = false, want true", v)
			}
			if unrecognized != "" {
				t.Errorf("On(%q) reported %q as unrecognized", v, unrecognized)
			}
		})
	}
}

// The off spellings are off and are NOT reported: an operator who writes
// `=false` said something the switch understands.
func TestOn_AcceptedOffSpellings(t *testing.T) {
	for _, v := range []string{"", "0", "false", "FALSE", "f", "no", "N", "off", "OFF", "  "} {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv(varName, v)
			on, unrecognized := On(varName)
			if on {
				t.Errorf("On(%q) = true, want false", v)
			}
			if unrecognized != "" {
				t.Errorf("On(%q) reported %q as unrecognized", v, unrecognized)
			}
		})
	}
}

// A value no spelling covers is off AND reported, so the caller can tell the
// operator the switch was ignored. Reporting is the whole point: the previous
// behaviour was to treat it as off in silence.
func TestOn_UnrecognizedValueIsReportedVerbatim(t *testing.T) {
	for _, v := range []string{"yep", "2", "enabled", "-1", "TRUE!"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(varName, v)
			on, unrecognized := On(varName)
			if on {
				t.Errorf("On(%q) = true; an unparseable value must not widen what the process may do", v)
			}
			if unrecognized != v {
				t.Errorf("On(%q) unrecognized = %q, want the raw value %q", v, unrecognized, v)
			}
		})
	}
}

// An unset variable is the ordinary case, not an operator mistake.
func TestOn_UnsetIsOffAndSilent(t *testing.T) {
	on, unrecognized := On("CTXLOOM_ENVSWITCH_DEFINITELY_UNSET")
	if on || unrecognized != "" {
		t.Errorf("On(unset) = (%v, %q), want (false, \"\")", on, unrecognized)
	}
}
