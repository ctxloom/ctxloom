package clidiag

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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

func TestWarnErrors_EmptyIsSuccess(t *testing.T) {
	restore := SetSink(new(bytes.Buffer))
	defer restore()
	if err := WarnErrors("ctxloom", nil); err != nil {
		t.Fatalf("WarnErrors(nil) = %v, want nil (no items => no failure)", err)
	}
}

func TestWarnErrors_NonEmptyWarnsAndFails(t *testing.T) {
	var b bytes.Buffer
	restore := SetSink(&b)
	defer restore()
	err := WarnErrors("ctxloom", []string{"backend a: boom", "backend b: kaboom"})
	if err == nil {
		t.Fatal("WarnErrors with items = nil, want a non-nil error so the command's exit code reflects the failure")
	}
	out := b.String()
	if !strings.Contains(out, "backend a: boom") || !strings.Contains(out, "backend b: kaboom") {
		t.Fatalf("WarnErrors must still print every warning, got %q", out)
	}
}

// TestFwarnOnce_DedupKeyIsProgScoped pins that FwarnOnce (via WarnOnce, its
// only production caller — clidiag.go:172-174) builds its dedup key with
// Line, which splices prog INTO the key, not just the format+args. This is
// the concrete internal wiring the U103-F01/F02 findings mischaracterized as
// DEAD: Line has zero callers OUTSIDE this package, but FwarnOnce (its sole
// caller) is on 12 production WarnOnce sites, and this test is what breaks
// if that wiring is ever removed or "simplified" to a prog-blind key.
func TestFwarnOnce_DedupKeyIsProgScoped(t *testing.T) {
	restore := SetSink(nil)
	defer restore()

	var buf bytes.Buffer
	SetSink(&buf)
	defer SetSink(nil)

	WarnOnce("ctxloom", "dial-home failed: %s", "timeout")
	WarnOnce("taskloom", "dial-home failed: %s", "timeout")

	got := strings.Count(buf.String(), "dial-home failed: timeout")
	if got != 2 {
		t.Fatalf("WarnOnce from two different progs with the same format+args should NOT dedup against each other (Line's key includes prog); got %d lines, want 2:\n%s", got, buf.String())
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

// large-album: Warn/WarnOnce wrote to os.Stderr unconditionally, and in
// `ctxloom run` stderr IS the terminal the harness paints its TUI on — so
// "run channel down (reconnecting)" and friends landed straight on the TUI and
// corrupted it. The sink must be redirectable for the lifetime of a session.
func TestSetSink_RedirectsWarnAndWarnOnce(t *testing.T) {
	t.Cleanup(func() { SetSink(nil) })

	var buf bytes.Buffer
	restore := SetSink(&buf)

	Warn("ctxloom", "run channel down (%s)", "reconnecting")
	assert.Contains(t, buf.String(), "run channel down (reconnecting)",
		"Warn must honour the redirected sink, not os.Stderr")

	buf.Reset()
	WarnOnce("ctxloom", "dial-home failed %d", 1)
	WarnOnce("ctxloom", "dial-home failed %d", 1)
	assert.Equal(t, 1, strings.Count(buf.String(), "dial-home failed 1"),
		"WarnOnce must honour the sink AND stay deduped through it")

	// Restoring puts the default back so a later command is unaffected.
	restore()
	buf.Reset()
	Warn("ctxloom", "after restore")
	assert.Empty(t, buf.String(), "after restore the sink must no longer receive warnings")
}

// TestSetSink_NilRestoresDefault guards the cleanup path: passing nil must
// return to stderr rather than installing a nil writer that panics on the next
// warning.
func TestSetSink_NilRestoresDefault(t *testing.T) {
	var buf bytes.Buffer
	SetSink(&buf)
	SetSink(nil)
	assert.NotPanics(t, func() { Warn("ctxloom", "back on stderr") })
	assert.Empty(t, buf.String())
}

// Line and fwarn are the two renderings of the SAME human warning — Line's doc
// says so ("one stable identity per distinct message regardless of which wire
// shape actually gets written"), and Line IS the dedup key FwarnOnce/WarnOnce
// compare on. They must therefore agree for every prog. Line spliced prog into
// the FORMAT STRING while fwarn passes it as a %s ARGUMENT, so a prog carrying a
// percent sign was reinterpreted as a verb in one path and not the other: the
// key and the emitted line diverge, and the stray verb can even consume the
// caller's args. (U103-F05.)
func TestLine_MatchesFwarnForAProgContainingAPercent(t *testing.T) {
	for _, prog := range []string{"ctxloom", "taskloom", "100%-native", "ctx%sloom", "a%!b"} {
		var b strings.Builder
		Fwarn(&b, prog, "no files match %q", "x")
		assert.Equal(t, b.String(), Line(prog, "no files match %q", "x"),
			"Line must render exactly what Fwarn writes for prog %q — it is that line's dedup key", prog)
	}
}

// SetSink's doc guarantees it "never installs a nil writer" — the guarantee that
// stops the next warning panicking or vanishing. The `w == nil` check only sees
// an UNTYPED nil, so a typed-nil (`var f *os.File; SetSink(f)`) satisfied w !=
// nil and was installed: writes to it either panic (*bytes.Buffer) or return
// ErrInvalid (*os.File), and fwarn discards the write error — the diagnostic is
// gone with no trace, this project's signature failure shape. A typed nil must
// be treated exactly like an untyped one: fall back to the default sink.
// (U103-F08. warnSink's identity is asserted rather than driving a real Warn,
// which would print onto the suite's own stderr.)
func TestSetSink_TypedNilFallsBackToTheDefault(t *testing.T) {
	var file *os.File
	var buf *bytes.Buffer
	var fn writerFunc

	for name, w := range map[string]io.Writer{
		"*os.File":      file,
		"*bytes.Buffer": buf,
		"func-based":    fn,
		"untyped nil":   nil,
	} {
		restore := SetSink(w)
		assert.Same(t, os.Stderr, warnSink(), "a %s nil writer must not be installed as the sink", name)
		restore()
	}
}

// writerFunc is a func-kinded io.Writer, so the typed-nil guard is exercised on
// something other than a pointer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
