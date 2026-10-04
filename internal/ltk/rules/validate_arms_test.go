package rules

import (
	"errors"
	"testing"
)

// TestValidateRuleArms characterizes every rejection rule validation can
// produce, by sentinel and not merely by err != nil, so a restructured
// validator is checkable: each arm must still fire, in the same order, for the
// same reason. Order matters here — several fixtures are constructed so that
// exactly one check can fire.
func TestValidateRuleArms(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       error
	}{
		{
			"missing id",
			"version: 1\nrules:\n  - match: { command: [go] }\n    message: m\n",
			ErrMissingID,
		},
		{
			"duplicate id",
			"version: 1\nrules:\n  - id: x\n    match: { command: [a] }\n    message: m\n  - id: x\n    match: { command: [b] }\n    message: m\n",
			ErrDuplicateID,
		},
		{
			"invalid action",
			"version: 1\nrules:\n  - id: x\n    action: nuke\n    match: { command: [go] }\n    message: m\n",
			ErrInvalidAction,
		},
		{
			"invalid mode",
			"version: 1\nrules:\n  - id: x\n    mode: loud\n    match: { command: [go] }\n    message: m\n",
			ErrInvalidMode,
		},
		{
			"no conditions",
			"version: 1\nrules:\n  - id: x\n    match: {}\n    message: m\n",
			ErrNoConditions,
		},
		{
			"path rule with no patterns",
			"version: 1\npath_rules:\n  - id: x\n    match: {}\n    message: m\n",
			ErrNoConditions,
		},
		{
			"path under rules",
			"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], command: [go] }\n    message: m\n",
			ErrRemovedField,
		},
		{
			"empty command pattern",
			"version: 1\nrules:\n  - id: x\n    match: { command: [go, \"\"] }\n    message: m\n",
			ErrEmptyPattern,
		},
		{
			"invalid path glob",
			"version: 1\npath_rules:\n  - id: x\n    match: { path: [\"[a\"] }\n    message: m\n",
			ErrInvalidGlob,
		},
		{
			"unknown shell",
			"version: 1\nrules:\n  - id: x\n    match: { command: [go], shells: [fish] }\n    message: m\n",
			ErrInvalidShell,
		},
		{
			"deny with nothing to say",
			"version: 1\nrules:\n  - id: x\n    match: { command: [go] }\n",
			ErrDenyUnexplained,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
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
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [VERSION] }\n    message: m\n",
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [\"@submodules\"] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    mode: disable\n    match: { command: [go] }\n",
		"version: 1\ndefaults: { repeat_window_seconds: 30 }\nrules:\n  - id: x\n    mode: confirm\n    match: { command: [go] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { command: [go], shells: [bash, zsh] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { command: [go] }\n    suggest: use just test\n",
	}
	for i, y := range cases {
		if _, err := Parse([]byte(y)); err != nil {
			t.Errorf("case %d: unexpected validation error: %v", i, err)
		}
	}
}
