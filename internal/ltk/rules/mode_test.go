package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// delay_seconds must fit inside a confirm window, or Parse rejects it.
func TestDelayValidation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"valid delay under window", "schema_version: 1\ndefaults: { repeat_window_seconds: 30 }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    delay_seconds: 10\n    message: m\n", false},
		{"delay equals window", "schema_version: 1\ndefaults: { repeat_window_seconds: 10 }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    delay_seconds: 10\n    message: m\n", true},
		{"delay without any window", "schema_version: 1\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    delay_seconds: 10\n    message: m\n", true},
		{"delay on non-confirm is ignored", "schema_version: 1\nrules:\n  - id: r\n    match: { command: [go, test] }\n    delay_seconds: 10\n    message: m\n", false},
		{"default delay under window", "schema_version: 1\ndefaults: { repeat_window_seconds: 30, repeat_delay_seconds: 10 }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    message: m\n", false},
		{"default delay equals window", "schema_version: 1\ndefaults: { repeat_window_seconds: 10, repeat_delay_seconds: 10 }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    message: m\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// A `mode: confirm` rule with no effective window can never be confirmed — it
// would silently behave like an inviolate `enable` rule — so Parse rejects it
// instead of inverting the author's escape hatch into a firm denial.
func TestConfirmWithoutWindowRejected(t *testing.T) {
	// no per-rule window and no global default → rejected.
	if _, err := Parse([]byte("schema_version: 1\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    message: m\n")); err == nil {
		t.Error("windowless confirm rule should be a validation error")
	}
	// a per-rule window makes it valid.
	if _, err := Parse([]byte("schema_version: 1\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    window_seconds: 30\n    message: m\n")); err != nil {
		t.Errorf("confirm rule with window_seconds should be valid: %v", err)
	}
	// a global default window makes it valid too.
	if _, err := Parse([]byte("schema_version: 1\ndefaults: { repeat_window_seconds: 30 }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    message: m\n")); err != nil {
		t.Errorf("confirm rule with default window should be valid: %v", err)
	}
	// a non-confirm rule with no window is unaffected.
	if _, err := Parse([]byte("schema_version: 1\nrules:\n  - id: r\n    match: { command: [go, test] }\n    message: m\n")); err != nil {
		t.Errorf("enable rule needs no window: %v", err)
	}
}

// mode controls whether a rule fires: disable → inert, enable/confirm → fires.
func TestRuleModeMatching(t *testing.T) {
	mk := func(mode Mode) *Config {
		cfg := mustParse(t, "schema_version: 1\nrules:\n  - id: no-go-test\n    match: { command: [go, test] }\n    message: use just test\n")
		cfg.Rules[0].Mode = mode
		return cfg
	}
	script := cmd(ir.ShellBash, "go", "test")

	cases := []struct {
		name    string
		mode    Mode
		allowed bool
	}{
		{"absent defaults to enable (fires)", "", false},
		{"enable fires", ModeEnable, false},
		{"confirm fires", ModeConfirm, false},
		{"disable is inert", ModeDisable, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := Evaluate(mk(tc.mode), script); d.Allowed != tc.allowed {
				t.Fatalf("allowed=%v want %v", d.Allowed, tc.allowed)
			}
		})
	}
}

// mode decodes from YAML, defaulting to enable when absent.
func TestModeParsesFromYAML(t *testing.T) {
	cfg, err := Parse([]byte(`schema_version: 1
rules:
  - id: off-rule
    match: { command: [go, test] }
    mode: disable
  - id: default-rule
    match: { command: [git, push], args_all: [--force] }
    message: force-pushing rewrites shared history
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rules[0].isEnabled() {
		t.Error("mode:disable should disable rule 0")
	}
	if cfg.Rules[1].mode() != ModeEnable {
		t.Error("absent mode should default to enable")
	}
}

func TestInvalidModeRejected(t *testing.T) {
	_, err := Parse([]byte("schema_version: 1\nrules:\n  - id: x\n    mode: sometimes\n    match: { command: [go] }\n"))
	if err == nil {
		t.Error("an unknown mode should be a validation error")
	}
}

// confirmPolicy: only confirm-mode rules are repeatable, and only with a window.
func TestConfirmPolicy(t *testing.T) {
	cases := []struct {
		name       string
		rule       RuleBase
		defs       Defaults
		repeatable bool
		window     int
		delay      int
	}{
		{"enable is inviolate", RuleBase{Mode: ModeEnable}, Defaults{RepeatWindowSeconds: 30}, false, 0, 0},
		{"disable is not repeatable", RuleBase{Mode: ModeDisable}, Defaults{RepeatWindowSeconds: 30}, false, 0, 0},
		{"confirm uses global window", RuleBase{Mode: ModeConfirm}, Defaults{RepeatWindowSeconds: 30}, true, 30, 0},
		{"confirm overrides window", RuleBase{Mode: ModeConfirm, WindowSeconds: 5}, Defaults{RepeatWindowSeconds: 30}, true, 5, 0},
		{"confirm with no window is inert", RuleBase{Mode: ModeConfirm}, Defaults{}, false, 0, 0},
		{"confirm carries per-rule delay", RuleBase{Mode: ModeConfirm, DelaySeconds: 10}, Defaults{RepeatWindowSeconds: 30}, true, 30, 10},
		{"confirm uses default delay", RuleBase{Mode: ModeConfirm}, Defaults{RepeatWindowSeconds: 30, RepeatDelaySeconds: 10}, true, 30, 10},
		{"per-rule delay overrides default", RuleBase{Mode: ModeConfirm, DelaySeconds: 5}, Defaults{RepeatWindowSeconds: 30, RepeatDelaySeconds: 10}, true, 30, 5},
		{"delay ignored for enable", RuleBase{Mode: ModeEnable, DelaySeconds: 10}, Defaults{RepeatWindowSeconds: 30}, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, win, delay := tc.rule.confirmPolicy(tc.defs)
			if rep != tc.repeatable || win != tc.window || delay != tc.delay {
				t.Fatalf("got (repeatable=%v, window=%d, delay=%d), want (%v, %d, %d)", rep, win, delay, tc.repeatable, tc.window, tc.delay)
			}
		})
	}
}
