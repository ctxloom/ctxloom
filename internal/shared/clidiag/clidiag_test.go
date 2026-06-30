package clidiag

import (
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	if got, want := Line("ctxloom", "no files match %q", "x"), "ctxloom: warning: no files match \"x\"\n"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
}

func TestFwarn(t *testing.T) {
	var b strings.Builder
	Fwarn(&b, "taskloom", "sync failed: %v", "boom")
	if got, want := b.String(), "taskloom: warning: sync failed: boom\n"; got != want {
		t.Fatalf("Fwarn = %q, want %q", got, want)
	}
}

func TestWarnerBindsProg(t *testing.T) {
	var b strings.Builder
	Fwarn(&b, string(Warner("ltk")), "bad rule %q", "x")
	if got := b.String(); !strings.HasPrefix(got, "ltk: warning: ") {
		t.Fatalf("Warner prefix wrong: %q", got)
	}
}
