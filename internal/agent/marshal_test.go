package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// hookEntry/hookGroup mirror the engine writers' structs whose field order
// (type-before-command, matcher-before-hooks) used to leak into the file.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}
type hookGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

func TestCanonicalJSON_SortsRecursivelyAndEndsInNewline(t *testing.T) {
	v := map[string]any{
		"statusLine": hookEntry{Type: "command", Command: "ctxloom hook hud"},
		"hooks": map[string]any{
			"PreToolUse": []hookGroup{{Matcher: "Bash", Hooks: []hookEntry{{Type: "command", Command: "ltk evaluate"}}}},
		},
	}
	out, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	if !strings.HasSuffix(s, "\n") {
		t.Error("canonical output must end in a trailing newline")
	}
	// Nested struct field order must NOT leak: "command" sorts before "type".
	if ci, ti := strings.Index(s, `"command"`), strings.Index(s, `"type"`); ci < 0 || ti < 0 || ci > ti {
		t.Errorf("keys not recursively sorted (command should precede type):\n%s", s)
	}
	// Top-level keys sorted: "hooks" before "statusLine".
	if hi, si := strings.Index(s, `"hooks"`), strings.Index(s, `"statusLine"`); hi > si {
		t.Errorf("top-level keys not sorted:\n%s", s)
	}
}

func TestCanonicalJSON_Idempotent(t *testing.T) {
	v := map[string]any{"b": 2, "a": []any{map[string]any{"y": 1, "x": 2}}}
	once, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	// Re-canonicalizing already-canonical bytes (as a generic value) is a no-op.
	var parsed any
	if err := json.Unmarshal(once, &parsed); err != nil {
		t.Fatal(err)
	}
	twice, err := CanonicalJSON(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Errorf("not idempotent:\n%q\n%q", once, twice)
	}
}
