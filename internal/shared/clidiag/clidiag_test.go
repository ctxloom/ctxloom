package clidiag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
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

// resetStructured restores the default (off) structured-diagnostics mode
// after a test that flips it, so package-level state never leaks into a
// later test.
func resetStructured(t *testing.T) {
	t.Cleanup(func() { SetStructured(false) })
}

func TestFwarn_StructuredModeEncodesWarningEnvelope(t *testing.T) {
	resetStructured(t)
	SetStructured(true)

	var b strings.Builder
	Fwarn(&b, "ctxloom", "fetch %s: %v", "origin", "timeout")

	var env clifmt.WarningEnvelope
	if err := json.Unmarshal([]byte(b.String()), &env); err != nil {
		t.Fatalf("Fwarn structured output not valid JSON: %v (%q)", err, b.String())
	}
	if env.Prog != "ctxloom" || env.Warning != "fetch origin: timeout" {
		t.Errorf("got envelope %+v, want {ctxloom fetch origin: timeout}", env)
	}
	if strings.Contains(b.String(), ": warning:") {
		t.Errorf("structured output leaked the human line format: %q", b.String())
	}
}

func TestFwarn_DefaultModeIsUnchangedHumanLine(t *testing.T) {
	resetStructured(t)
	// structured defaults to false; asserted explicitly here in case an
	// earlier test in this package forgot its own cleanup.
	SetStructured(false)

	var b strings.Builder
	Fwarn(&b, "taskloom", "sync failed: %v", "boom")
	if got, want := b.String(), "taskloom: warning: sync failed: boom\n"; got != want {
		t.Fatalf("Fwarn = %q, want %q", got, want)
	}
}

func TestFwarnOnce_DedupsAcrossStructuredMode(t *testing.T) {
	resetStructured(t)
	SetStructured(true)

	var b strings.Builder
	FwarnOnce(&b, "ctxloom", "dedup case %q", "structured-first")
	FwarnOnce(&b, "ctxloom", "dedup case %q", "structured-first")

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (dedup should still collapse in structured mode): %q", len(lines), b.String())
	}
	var env clifmt.WarningEnvelope
	if err := json.Unmarshal([]byte(lines[0]), &env); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, lines[0])
	}
}
