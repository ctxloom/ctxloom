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

// TestCanonicalJSON_PreservesLargeIntegers pins the data-integrity invariant:
// a number that arrives as an exact JSON literal leaves as the SAME literal.
// The canonicaliser re-decodes to sort keys, and a generic decode that lands
// numbers in float64 silently rewrites any integer past 2^53 — for a writer
// whose whole job is persisting the user's file, that is corruption with a
// success exit code.
func TestCanonicalJSON_PreservesLargeIntegers(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"int64 past float64's exact range", `1234567890123456789`},
		{"math.MaxInt64", `9223372036854775807`},
		{"math.MinInt64", `-9223372036854775808`},
		{"uint64 past MaxInt64", `18446744073709551615`},
		{"magnitude no float64 can hold", `1e1000`},
		{"a preserved decimal's own digits", `3.141592653589793238462643383`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := CanonicalJSON(map[string]json.RawMessage{"n": json.RawMessage(tc.in)})
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			want := "{\n  \"n\": " + tc.in + "\n}\n"
			if string(out) != want {
				t.Errorf("number rewritten on the way to disk:\n got %q\nwant %q", out, want)
			}
		})
	}
}

// TestCanonicalJSON_PreservesLargeIntegersInStructs covers the same invariant
// on the other shape the writers use: a Go struct whose int64 field holds a
// value the generic re-decode would round.
func TestCanonicalJSON_PreservesLargeIntegersInStructs(t *testing.T) {
	out, err := CanonicalJSON(struct {
		N int64 `json:"n"`
	}{1234567890123456789})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"n\": 1234567890123456789\n}\n"; string(out) != want {
		t.Errorf("int64 field rewritten on the way to disk:\n got %q\nwant %q", out, want)
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
