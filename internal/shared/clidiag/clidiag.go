// Package clidiag is the ctxloom family's stderr diagnostic convention:
// fault-tolerant warnings prefixed "<prog>: warning:" in text/markdown
// mode, or one clifmt.WarningEnvelope JSON-Lines object per warning when
// structured mode is on (see SetStructured). Per the fault-tolerance
// philosophy, components warn and continue rather than crash, so this is the
// one place that owns both wire shapes. The prog is a parameter (not hardcoded
// to "ctxloom") so every binary stamps its own name.
package clidiag

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Line returns the "<prog>: warning: <msg>\n" line without writing it, for
// callers that need the string itself — dedup keys, or emission deferred to an
// aggregating writer. It always returns the human line, even in structured
// mode: WarnOnce/FwarnOnce's dedup key (see onceSeen below) needs one stable identity
// per distinct message regardless of which wire shape actually gets written.
//
// prog is passed as an ARGUMENT, never spliced into the format string, so it
// renders identically to fwarn's "%s: warning: %s\n" for every prog. Splicing it
// made a percent sign in prog a format verb in this path only: the key and the
// emitted line diverged, and the stray verb consumed the caller's args, so the
// MESSAGE BODY came out mangled too.
func Line(prog, format string, args ...any) string {
	return fmt.Sprintf("%s: warning: %s\n", prog, fmt.Sprintf(format, args...))
}

// structured gates whether Fwarn/FwarnOnce write the human "<prog>: warning:
// <msg>" line or a clifmt.WarningEnvelope JSON-Lines object. Off by default,
// so every existing caller — including taskloom and ltk, which don't parse a
// --format flag yet — keeps today's plain-text stderr behavior; only the CLI
// root command flips it on, once, after resolving --format to json/yaml/toml
// (see cli's PersistentPreRun and clifmt.Format.Structured). A process-wide
// flag rather than a parameter threaded through clidiag's 100+ call sites —
// many several layers below any single command's cobra.Command, in
// coordinator daemons, isolation runners, and background gRPC servers that
// have no cobra.Command to read a per-invocation format from — because the
// choice is which wire shape THIS process's stderr speaks for the lifetime
// of one CLI invocation, not something each individual warn site decides.
var structured atomic.Bool

// SetStructured turns the process-wide structured-diagnostics channel on or
// off. Call once, before command work starts.
func SetStructured(on bool) {
	structured.Store(on)
}

// Fwarn writes a "<prog>: warning: <msg>" line to w — or, when structured
// mode is on, a clifmt.WarningEnvelope as one compact JSON object (see
// clifmt.EncodeWarning's doc for why the channel is always JSON Lines
// regardless of the primary --format's json/yaml/toml choice). Best-effort:
// the write error is dropped (warnings never block), but a wrapping writer
// that records its own errors (e.g. errwriter.Writer) still observes the
// failure.
func Fwarn(w io.Writer, prog, format string, args ...any) {
	fwarn(w, prog, fmt.Sprintf(format, args...), "")
}

// fwarn writes msg (already formatted) and its remedy ("" for an advisory)
// to w in whichever wire shape structured mode currently selects: the text
// line followed by clifmt.FixLine, or a WarningEnvelope carrying Remedy.
// Every warn helper funnels here so the branch lives in exactly one place.
func fwarn(w io.Writer, prog, msg, remedy string) {
	if structured.Load() {
		_ = clifmt.EncodeWarning(w, clifmt.WarningEnvelope{Prog: prog, Warning: msg, Remedy: remedy})
		return
	}
	_, _ = fmt.Fprintf(w, "%s: warning: %s%s\n", prog, msg, clifmt.FixLine("  ", remedy))
}

// sinkEntry is ONE active redirect. A nil w means "the default (os.Stderr)", so
// SetSink(nil) is an explicit redirect back to the default rather than an absent
// entry. Identity is the POINTER, never the writer: two redirects to the same
// writer — or two SetSink(nil) calls — are still distinct entries, so a restore
// can remove exactly the one it installed.
type sinkEntry struct{ w io.Writer }

// sinkStack holds every redirect that has NOT yet been restored; its top is the
// active sink, and an empty stack means os.Stderr.
//
// The redirect machinery exists because os.Stderr is NOT always a safe place to
// write: under `ctxloom run` stderr IS the terminal the harness paints its TUI
// on, so an unconditional warning corrupts the display mid-frame —
// "run channel down (reconnecting)" landed straight on the TUI. A session that
// owns the terminal redirects the sink for its lifetime instead.
//
// It is a stack because restores are not guaranteed to arrive in LIFO order: an
// owner that finishes early must not take the channel away from an owner that
// is still painting, and a later restore must not resurrect a sink whose owner
// has already closed it.
//
// sinkMu guards the stack AND every write to the active sink (see warnToSink).
// Resolving the sink and writing to it happen under one hold, so:
//   - concurrent warnings never race or interleave, whatever writer is
//     installed — the writer need not be safe for concurrent use. Tests
//     install a plain bytes.Buffer, and goroutines a test started (or an
//     earlier test left running) warn into it concurrently;
//   - once SetSink's restore returns, no write to the restored writer is in
//     flight or can start, so its owner may read or close it without racing.
//
// The cost is that a slow sink write delays every other sink warning and every
// SetSink/restore; warnings are rare, and the only writer that must stay fast
// is the one already chosen to keep stderr clean. No code may warn through
// clidiag while holding sinkMu — fwarn reaches only the writer and clifmt,
// neither of which calls back into clidiag — or it deadlocks.
var (
	sinkMu    sync.Mutex
	sinkStack []*sinkEntry
)

// SetSink redirects Warn/WarnOnce to w until the returned restore func runs. A
// nil w (including a typed nil) redirects to the default, os.Stderr — it never
// installs a nil writer.
//
// Restore removes only THIS redirect and hands the channel to whichever redirect
// is still active, so overlapping redirects unwound in any order are safe: an
// early restore cannot steal a live redirect, and a late one cannot resurrect a
// finished one. It is idempotent — calling it twice pops nothing further. It
// returns only after any warning already writing to w has finished, and no
// later warning reaches w.
//
// Only Warn/WarnOnce move; the explicit Fwarn/FwarnOnce writers are
// untouched, because a caller that named its own writer already chose.
func SetSink(w io.Writer) (restore func()) {
	if isNilWriter(w) {
		w = nil
	}
	e := &sinkEntry{w: w}

	sinkMu.Lock()
	sinkStack = append(sinkStack, e)
	sinkMu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { popSink(e) }) }
}

// popSink removes e from the redirect stack, wherever it sits.
func popSink(e *sinkEntry) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	for i := len(sinkStack) - 1; i >= 0; i-- {
		if sinkStack[i] == e {
			sinkStack = append(sinkStack[:i], sinkStack[i+1:]...)
			return
		}
	}
}

// isNilWriter reports whether w carries no usable writer — an untyped nil, or a
// TYPED nil (`var f *os.File; SetSink(f)`), which a bare `w == nil` misses
// because the interface still holds a type. Installing one is the failure SetSink
// documents itself as preventing: writing to it either panics (*bytes.Buffer) or
// returns an error fwarn discards (*os.File), so the diagnostic disappears
// without a trace.
func isNilWriter(w io.Writer) bool {
	if w == nil {
		return true
	}
	switch v := reflect.ValueOf(w); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

// activeSink resolves the current destination: the top redirect, or os.Stderr
// when none is installed or the top is an explicit redirect back to the
// default. The caller holds sinkMu.
func activeSink() io.Writer {
	if n := len(sinkStack); n > 0 && sinkStack[n-1].w != nil {
		return sinkStack[n-1].w
	}
	return os.Stderr
}

// warnToSink writes msg to the active sink, holding sinkMu across both the
// resolution and the write (see sinkStack for why).
func warnToSink(prog, msg, remedy string) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	fwarn(activeSink(), prog, msg, remedy)
}

// Warn prints a "<prog>: warning: <msg>" line to the current sink (stderr by
// default — see SetSink).
func Warn(prog, format string, args ...any) {
	warnToSink(prog, fmt.Sprintf(format, args...), "")
}

// WarnRemedy is Warn for a warning that names its fix: the text line is
// followed by clifmt.FixLine, and the structured envelope carries Remedy.
func WarnRemedy(prog, remedy, format string, args ...any) {
	warnToSink(prog, fmt.Sprintf(format, args...), remedy)
}

// WarnErrors is the shared seam for turning a partial-failure result (a
// command that collected per-item errors — e.g. per-backend hook-apply
// failures — while still doing everything it could) into BOTH a warning per
// item AND a non-zero process exit. Call this instead of the ad hoc
//
//	for _, e := range result.Errors {
//	    clidiag.Warn(prog, "%s", e)
//	}
//	return nil
//
// pattern ("exit 0 on a real failure"): that shape prints the same
// warnings but always returns nil, so cli.Run never learns the command
// failed. WarnErrors prints identically, then returns a non-nil error when
// errs is non-empty (nil when it's empty), so the caller can simply
// `return clidiag.WarnErrors(prog, result.Errors)` and let cli.Run's
// existing RunE-error handling turn it into exit code 1. It invents no new
// exit-code taxonomy — every WarnErrors failure maps to the same code any
// other command error already does.
func WarnErrors(prog string, errs []string) error {
	for _, e := range errs {
		Warn(prog, "%s", e)
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%s", errs[0])
	default:
		return fmt.Errorf("%d errors; see warnings above", len(errs))
	}
}

// onceSeen dedups WarnOnce/FwarnOnce lines per process, keyed by the full
// formatted line (Line's doc calls this out as its dedup-key use).
var (
	onceMu   sync.Mutex
	onceSeen = map[string]struct{}{}
)

// FwarnOnce writes the warning to w at most once per process for identical
// formatted content, in whichever wire shape structured mode currently
// selects (see Fwarn). Repeat diagnostics from independently constructed
// components — e.g. every subsystem building its own profile loader and
// re-hitting the same unresolvable parent — collapse to a single line
// instead of spamming startup. Best-effort like Fwarn.
func FwarnOnce(w io.Writer, prog, format string, args ...any) {
	warnOnce(func(prog, msg, remedy string) { fwarn(w, prog, msg, remedy) }, prog, "", format, args...)
}

// warnOnce hands the message to emit unless an identical line was already
// emitted. The dedup key is the message line alone: one fault is one warning,
// whichever fix it names. emit runs under onceMu, so lock order is onceMu then
// sinkMu (when emit is warnToSink); nothing takes them in the other order.
func warnOnce(emit func(prog, msg, remedy string), prog, remedy, format string, args ...any) {
	key := Line(prog, format, args...)
	onceMu.Lock()
	defer onceMu.Unlock()
	if _, seen := onceSeen[key]; seen {
		return
	}
	onceSeen[key] = struct{}{}
	emit(prog, fmt.Sprintf(format, args...), remedy)
}

// WarnOnce prints a "<prog>: warning: <msg>" line to the current sink (stderr
// by default — see SetSink) at most once per process for identical formatted
// content.
func WarnOnce(prog, format string, args ...any) {
	warnOnce(warnToSink, prog, "", format, args...)
}

// WarnRemedyOnce is WarnOnce carrying a remedy (see WarnRemedy).
func WarnRemedyOnce(prog, remedy, format string, args ...any) {
	warnOnce(warnToSink, prog, remedy, format, args...)
}

// ResetWarnOnce clears onceSeen, WarnOnce/FwarnOnce's process-wide dedup
// memory. Test seam only: production code relies on the dedup surviving for
// the whole process, exactly the "once per process" WarnOnce documents
// itself as providing, so nothing but a test calls this. Without it, a test
// that pins WarnOnce's once-per-message behavior is only reliable the FIRST
// time its process runs it — `go test -count=2` (and any other harness that
// re-executes a test function inside one process) reuses the memory from the
// first run, so an identical warning fired on the second run is silently
// deduped away and a sink-content assertion sees nothing. Same shape as
// strictness.Reset's onceRecorded clear.
func ResetWarnOnce() {
	onceMu.Lock()
	defer onceMu.Unlock()
	onceSeen = map[string]struct{}{}
}
