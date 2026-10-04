package rules

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// The confirm-by-repeat override's known store race (see state.Store) is
// accepted only because its window is capped: an EFFECTIVE confirm window
// above MaxConfirmWindowSeconds — from defaults or a per-rule override, on
// either rule list — is refused at load, never clamped. A configured window
// that no confirm rule ever resolves to is not an override window, so it is
// not refused.
func TestConfirmWindowCap(t *testing.T) {
	over := strconv.Itoa(MaxConfirmWindowSeconds + 1)
	at := strconv.Itoa(MaxConfirmWindowSeconds)
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"default window over the cap", "defaults: { repeat_window_seconds: " + over + " }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    message: m\n", true},
		{"per-rule window over the cap", "rules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    window_seconds: " + over + "\n    message: m\n", true},
		{"path rule window over the cap", "path_rules:\n  - id: p\n    match: { path: [VERSION] }\n    mode: confirm\n    window_seconds: " + over + "\n    message: m\n", true},
		{"per-rule window over the cap beats a legal default", "defaults: { repeat_window_seconds: 10 }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    window_seconds: " + over + "\n    message: m\n", true},
		{"window exactly at the cap", "defaults: { repeat_window_seconds: " + at + " }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    window_seconds: " + at + "\n    message: m\n", false},
		{"oversized default overridden per rule is not effective", "defaults: { repeat_window_seconds: " + over + " }\nrules:\n  - id: r\n    match: { command: [go, test] }\n    mode: confirm\n    window_seconds: 10\n    message: m\n", false},
		{"oversized window on a non-confirm rule is ignored", "rules:\n  - id: r\n    match: { command: [go, test] }\n    window_seconds: " + over + "\n    message: m\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Parse: unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrConfirmWindowTooLong) {
				t.Fatalf("Parse err = %v, want ErrConfirmWindowTooLong", err)
			}
			var re *RuleError
			if !errors.As(err, &re) {
				t.Fatalf("Parse err = %T, want *RuleError naming the rule", err)
			}
			if !strings.Contains(err.Error(), at) {
				t.Errorf("error %q does not name the limit %s", err, at)
			}
		})
	}
}
