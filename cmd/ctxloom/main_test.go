package main

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// A logger constructor that fails must still yield a usable *zap.Logger.
// zap's constructors return (nil, err), and a nil logger is not inert: it is
// installed process-wide by zap.ReplaceGlobals, so the first zap.L() call
// anywhere in the tree dereferences nil, and main's own Sync does too. A
// diagnostics failure must stay a diagnostics failure rather than becoming a
// crash in unrelated code.
func TestBuildLogger_ConstructorFailureYieldsUsableLogger(t *testing.T) {
	var warn strings.Builder
	lg := buildLogger(func() (*zap.Logger, error) { return nil, errors.New("sink unavailable") }, &warn)
	if lg == nil {
		t.Fatal("buildLogger returned nil for a failing constructor")
	}
	// Both of these panic against a nil *zap.Logger.
	lg.Warn("still usable")
	_ = lg.Sync()
	if !strings.Contains(warn.String(), "sink unavailable") {
		t.Errorf("construction failure was not reported; warn stream = %q", warn.String())
	}
}

// The same hazard reachable without an error: a constructor that hands back a
// nil logger and a nil error. The guard is on the VALUE, not only on the error.
func TestBuildLogger_NilWithoutErrorAlsoFallsBack(t *testing.T) {
	var warn strings.Builder
	lg := buildLogger(func() (*zap.Logger, error) { return nil, nil }, &warn)
	if lg == nil {
		t.Fatal("buildLogger returned nil for a constructor that returned (nil, nil)")
	}
	lg.Warn("still usable")
}

// A working constructor is passed through untouched and reports nothing: the
// fallback must not shadow the real logger or add noise to a normal startup.
func TestBuildLogger_PassesThroughSuccess(t *testing.T) {
	var warn strings.Builder
	want := zap.NewNop()
	got := buildLogger(func() (*zap.Logger, error) { return want, nil }, &warn)
	if got != want {
		t.Errorf("buildLogger replaced a working logger: got %p, want %p", got, want)
	}
	if warn.String() != "" {
		t.Errorf("successful construction wrote to the warn stream: %q", warn.String())
	}
}

// A nil warn stream is not a reason to hand back a nil logger — the fallback
// must survive having nowhere to report to.
func TestBuildLogger_NilWarnStreamStillFallsBack(t *testing.T) {
	lg := buildLogger(func() (*zap.Logger, error) { return nil, errors.New("boom") }, nil)
	if lg == nil {
		t.Fatal("buildLogger returned nil when the warn stream was nil")
	}
	lg.Warn("still usable")
}

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
