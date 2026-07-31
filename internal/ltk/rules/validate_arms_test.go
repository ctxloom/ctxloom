package rules

import (
	"strings"
	"testing"
)

// TestValidateRuleArms characterizes every rejection validateRule can produce,
// by the exact message text and not merely by err != nil. It is the safety net
// U073-F12's split needs: a pure complexity reduction cannot be shown by a red
// test, so what makes the refactor checkable is that each arm still fires, in
// the same order, with the same words. Order matters here — several fixtures
// are constructed so that exactly one check can fire.
func TestValidateRuleArms(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{
			"missing id",
			"version: 1\nrules:\n  - match: { command: go }\n    message: m\n",
			"rule #0: missing id",
		},
		{
			"duplicate id",
			"version: 1\nrules:\n  - id: x\n    match: { command: a }\n    message: m\n  - id: x\n    match: { command: b }\n    message: m\n",
			`duplicate rule id "x"`,
		},
		{
			"invalid action",
			"version: 1\nrules:\n  - id: x\n    action: nuke\n    match: { command: go }\n    message: m\n",
			`invalid action "nuke"`,
		},
		{
			"invalid mode",
			"version: 1\nrules:\n  - id: x\n    mode: loud\n    match: { command: go }\n    message: m\n",
			`invalid mode "loud"`,
		},
		{
			"no conditions",
			"version: 1\nrules:\n  - id: x\n    match: {}\n    message: m\n",
			"match has no conditions",
		},
		{
			"command mixed with path",
			"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], command: [go] }\n    message: m\n",
			"match.path cannot be combined with command/args/shells",
		},
		{
			"empty command token",
			"version: 1\nrules:\n  - id: x\n    match: { command: [go, \"\"] }\n    message: m\n",
			"empty token in match.command",
		},
		{
			"invalid path glob",
			"version: 1\nrules:\n  - id: x\n    match: { path: [\"[a\"] }\n    message: m\n",
			"invalid glob in match.path",
		},
		{
			"unknown shell",
			"version: 1\nrules:\n  - id: x\n    match: { command: [go], shells: [fish] }\n    message: m\n",
			`unknown shell "fish" in match.shells`,
		},
		{
			"deny with nothing to say",
			"version: 1\nrules:\n  - id: x\n    match: { command: [go] }\n",
			"a deny rule needs a message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected a validation error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestValidateRuleAcceptsTheWellFormedShapes is the other half of the safety
// net: a split that made validateRule stricter would show up here rather than
// in the rejection table.
func TestValidateRuleAcceptsTheWellFormedShapes(t *testing.T) {
	cases := []string{
		"version: 1\nrules:\n  - id: x\n    match: { command: [go, test] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { command: [go] }\n    action: allow\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [\"@submodules\"] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    mode: disable\n    match: { command: [go] }\n",
		"version: 1\ndefaults: { repeat_window_seconds: 60 }\nrules:\n  - id: x\n    mode: confirm\n    match: { command: [go] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { command: [go], shells: [bash, zsh] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { command: [go] }\n    suggest: use just test\n",
	}
	for i, y := range cases {
		if _, err := Parse([]byte(y)); err != nil {
			t.Errorf("case %d: unexpected validation error: %v", i, err)
		}
	}
}
