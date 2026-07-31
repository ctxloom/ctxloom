package clidiag

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
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

// SetSink's restore closure did an UNCONDITIONAL Store(prev), which is only
// correct if every redirect is unwound in strict LIFO order. Two overlapping
// redirects broke it both ways: the earlier owner's restore CLOBBERED a redirect
// that was still live (warnings escape onto the terminal the later owner is
// painting — the exact corruption SetSink exists to prevent), and the later
// owner's restore then RESURRECTED the earlier sink after its owner was gone
// (in production that writer is an *os.File the owner closes right after
// restoring, so the warning is written to a closed fd and the error discarded).
// Restore must remove only its OWN redirect and hand the channel to whichever
// redirect is still active. (U103-F06.)
func TestSetSink_OutOfOrderRestoreLeavesTheLiveRedirectAlone(t *testing.T) {
	var early, late bytes.Buffer
	restoreEarly := SetSink(&early)
	restoreLate := SetSink(&late)
	t.Cleanup(func() { restoreEarly(); restoreLate() })

	// The fixture must actually be overlapping-and-out-of-order from SetSink's
	// point of view before anything is asserted: `late` owns the channel now.
	require.Same(t, &late, warnSink(), "the later redirect must own the channel before the out-of-order restore")

	restoreEarly() // the EARLIER owner finishes first

	Warn("ctxloom", "late still owns the terminal")
	assert.Contains(t, late.String(), "late still owns the terminal",
		"an out-of-order restore must not steal a redirect that is still live")
	assert.Empty(t, early.String(), "the restored redirect must receive nothing more")

	restoreLate()
	assert.Same(t, os.Stderr, warnSink(),
		"once every redirect is restored the default is back — never a resurrected earlier sink")
}

// Restore is idempotent: calling it twice must not pop somebody else's redirect.
// Several tests in this tree both `defer restore()` and register a cleanup that
// restores again. (U103-F06.)
func TestSetSink_RestoreIsIdempotent(t *testing.T) {
	var outer, inner bytes.Buffer
	restoreOuter := SetSink(&outer)
	t.Cleanup(restoreOuter)

	restoreInner := SetSink(&inner)
	restoreInner()
	restoreInner()
	restoreInner()

	Warn("ctxloom", "outer still owns the channel")
	assert.Contains(t, outer.String(), "outer still owns the channel",
		"repeat restores must be no-ops, not repeated pops")
}

// failingWriter always refuses, so a warning's write error is guaranteed to
// happen rather than merely be possible.
type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// fwarn drops its write error DELIBERATELY — "warnings never block" is the
// fault-tolerance rule this package exists to implement, and the documented
// escape hatch is that a writer which records its own errors still observes the
// failure. U103-F07 claims the structured branch is different, leaving "no trace
// anywhere". It is NOT: clifmt.EncodeWarning is json.Encoder.Encode, which
// marshals a two-string envelope (never a marshal error) and performs exactly
// one w.Write — so the wrapping writer sees the failure in structured mode on
// exactly the same terms as in text mode. This test is the evidence for that
// refutation; it goes red if either branch ever stops writing through w.
func TestFwarn_WriteFailureIsObservableThroughAWrappingWriterInBothModes(t *testing.T) {
	resetStructured(t)

	boom := errors.New("sink is gone")

	for _, structuredOn := range []bool{false, true} {
		SetStructured(structuredOn)

		// The fixture must genuinely refuse the write, or "no error recorded"
		// would mean "nothing was attempted" rather than "nothing was seen".
		ew := iox.NewErrWriter(failingWriter{err: boom})
		n, err := ew.Write([]byte("probe"))
		require.ErrorIs(t, err, boom, "the fixture writer must actually fail")
		require.Zero(t, n)
		ew = iox.NewErrWriter(failingWriter{err: boom})

		Fwarn(ew, "ctxloom", "sync failed: %v", "timeout")
		require.ErrorIs(t, ew.Err(), boom,
			"structured=%v: a wrapping writer must still observe the dropped write error", structuredOn)
	}
}

// The other half of the same contract: a failing sink must never block, panic or
// propagate. This is what makes dropping the error the right call rather than an
// oversight, and it is why the remedy U103-F07 implies (surface the error from
// the write path) cannot simply be applied — Warn has 448 call sites, none of
// which can act on it, and the one place a fallback could go is os.Stderr, which
// under `ctxloom run` is the TUI SetSink exists to protect. (U103-F07.)
func TestWarn_FailingSinkNeverBlocksOrPanics(t *testing.T) {
	resetStructured(t)
	restore := SetSink(failingWriter{err: errors.New("sink is gone")})
	defer restore()

	assert.NotPanics(t, func() {
		Warn("ctxloom", "first")
		WarnOnce("ctxloom", "second")
		SetStructured(true)
		Warn("ctxloom", "third")
	})
}

// U103-F09's PREMISE — "the value is a per-binary constant", so prog could be
// hoisted out of the signature — is false, and this pins the callers that make it
// false. Inside the ctxloom binary ALONE two distinct progs are in production
// use: "ctxloom" and "ctxloom hook inject-context" (internal/cli's
// inject-context hook names itself so a warning surfacing inside a Claude Code
// hook is attributable to the hook rather than to the CLI). And
// internal/shared/confload — shared by ctxloom, taskloom, harp and ltk — passes
// its caller's own Program.Name, which its doc states outright.
//
// prog therefore carries information no per-binary constant holds, in the
// rendered line AND in the dedup key. This test is what a "simplify prog away"
// change has to break first.
func TestProg_IsNotAPerBinaryConstant(t *testing.T) {
	resetStructured(t)

	progs := []string{"ctxloom", "ctxloom hook inject-context", "taskloom", "ltk", "harp"}

	// (1) the rendered line carries the CALLER's prog, not one global name.
	for _, prog := range progs {
		var b strings.Builder
		Fwarn(&b, prog, "sync failed: %v", "boom")
		assert.Equal(t, prog+": warning: sync failed: boom\n", b.String())
	}

	// (2) so does the structured envelope.
	SetStructured(true)
	for _, prog := range progs {
		var b strings.Builder
		Fwarn(&b, prog, "sync failed: %v", "boom")
		var env clifmt.WarningEnvelope
		require.NoError(t, json.Unmarshal([]byte(b.String()), &env))
		assert.Equal(t, prog, env.Prog, "the envelope's prog field is the caller's, not a constant")
	}
	SetStructured(false)

	// (3) and the dedup key is prog-scoped, so two progs reporting the SAME
	// condition are two reports, not one silenced by the other.
	var b strings.Builder
	for _, prog := range progs {
		FwarnOnce(&b, prog, "same condition %d", 7)
	}
	assert.Equal(t, len(progs), strings.Count(b.String(), "same condition 7"),
		"collapsing prog into a constant would silence every binary but the first")
}
