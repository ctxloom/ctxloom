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

func TestFwarnOnce_DedupsIdenticalLines(t *testing.T) {
	var b strings.Builder
	FwarnOnce(&b, "ctxloom", "dedup case %q", "clidiag-once-first")
	FwarnOnce(&b, "ctxloom", "dedup case %q", "clidiag-once-first")
	if got, want := b.String(), "ctxloom: warning: dedup case \"clidiag-once-first\"\n"; got != want {
		t.Fatalf("FwarnOnce = %q, want single line %q", got, want)
	}
	// A different formatted line still emits.
	FwarnOnce(&b, "ctxloom", "dedup case %q", "clidiag-once-second")
	if got := b.String(); strings.Count(got, "\n") != 2 {
		t.Fatalf("distinct line should emit, got %q", got)
	}
}

func TestWarnerBindsProg(t *testing.T) {
	var b strings.Builder
	Fwarn(&b, string(Warner("ltk")), "bad rule %q", "x")
	if got := b.String(); !strings.HasPrefix(got, "ltk: warning: ") {
		t.Fatalf("Warner prefix wrong: %q", got)
	}
}
