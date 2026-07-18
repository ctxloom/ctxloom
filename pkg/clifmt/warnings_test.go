package clifmt

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeWarningJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeWarning(&buf, WarningEnvelope{Prog: "ctxloom", Warning: "disk full"}); err != nil {
		t.Fatalf("EncodeWarning: %v", err)
	}
	want := `{"prog":"ctxloom","warning":"disk full"}` + "\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestEncodeWarningIsOneLinePerCall(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeWarning(&buf, WarningEnvelope{Prog: "ctxloom", Warning: "first"}); err != nil {
		t.Fatalf("EncodeWarning: %v", err)
	}
	if err := EncodeWarning(&buf, WarningEnvelope{Prog: "ctxloom", Warning: "second"}); err != nil {
		t.Fatalf("EncodeWarning: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var env WarningEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("line %d not independently parseable JSON: %v (%q)", i, err, line)
		}
	}
}

func TestFormatStructured(t *testing.T) {
	cases := []struct {
		f    Format
		want bool
	}{
		{FormatJSON, true},
		{FormatYAML, true},
		{FormatTOML, true},
		{FormatText, false},
		{FormatMarkdown, false},
	}
	for _, c := range cases {
		if got := c.f.Structured(); got != c.want {
			t.Errorf("%s.Structured() = %v, want %v", c.f, got, c.want)
		}
	}
}
