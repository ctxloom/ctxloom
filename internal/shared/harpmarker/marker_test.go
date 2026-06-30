package harpmarker

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFormat(t *testing.T) {
	if got := Format("plump-loose-sash"); got != `<ctxloom name="plump-loose-sash" kind="harp" />` {
		t.Fatalf("Format = %q", got)
	}
	if got := Format(""); got != "" {
		t.Fatalf("Format(\"\") = %q, want empty", got)
	}
}

func TestFind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean marker", `prose <ctxloom name="swift-amber-falcon" kind="harp" /> more`, "swift-amber-falcon"},
		{"attr order kind first", `<ctxloom kind="harp" name="bold-crimson-thunder" />`, "bold-crimson-thunder"},
		{"no marker", `<ctxloom-context>body</ctxloom-context>`, ""},
		{"wrong kind", `<ctxloom name="x" kind="resumed-from" />`, ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Find(tc.in); got != tc.want {
				t.Fatalf("Find(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// jsonString marshals s as a JSON string value (with default HTML escaping, so
// '<' becomes <), matching what encoding/json emits in the real hook path.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScan_DirectAdditionalContext(t *testing.T) {
	// additionalContext delivered as a single-nested hook output string.
	marker := Format("single-nest-harp")
	hookOut := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":` + jsonString(t, marker) + `}}`
	line := `{"type":"system","content":` + jsonString(t, hookOut) + `}`
	if got := Scan([]byte(line)); got != "single-nest-harp" {
		t.Fatalf("Scan single-nest = %q", got)
	}
}

func TestScan_AttachmentDoubleNested(t *testing.T) {
	// The real Claude shape: attachment.stdout holds the hook's JSON output,
	// itself holding additionalContext — two layers of JSON escaping over the
	// marker, whose '<'/'>' are already </> from the hook encoder.
	marker := Format("plump-loose-sash")
	hookOut := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":` + jsonString(t, marker) + `}}`
	line := `{"parentUuid":null,"isSidechain":false,"attachment":{"type":"hook_success","hookName":"SessionStart:clear","stdout":` + jsonString(t, hookOut) + `}}`

	// Sanity: the raw line must NOT contain the clean marker (it is escaped),
	// so Scan is genuinely exercising the decode path, not a raw substring hit.
	if bytes.Contains([]byte(line), []byte(marker)) {
		t.Fatal("fixture leaks an unescaped marker; test would not exercise decoding")
	}
	if got := Scan([]byte(line)); got != "plump-loose-sash" {
		t.Fatalf("Scan double-nest = %q, want plump-loose-sash", got)
	}
}

func TestScan_NoMarker(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":"just some work, no marker"}}`
	if got := Scan([]byte(line)); got != "" {
		t.Fatalf("Scan no-marker = %q, want empty", got)
	}
}

func TestScan_MalformedJSON(t *testing.T) {
	if got := Scan([]byte(`not json at all`)); got != "" {
		t.Fatalf("Scan malformed = %q, want empty", got)
	}
}
