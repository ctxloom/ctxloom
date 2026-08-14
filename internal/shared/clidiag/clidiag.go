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
// that records its own errors (e.g. iox.ErrWriter) still observes the
// failure.
func Fwarn(w io.Writer, prog, format string, args ...any) {
	fwarn(w, prog, fmt.Sprintf(format, args...))
}

// fwarn writes msg (already formatted) to w in whichever wire shape
// structured mode currently selects. Shared by Fwarn and FwarnOnce so the
// branch lives in exactly one place.
func fwarn(w io.Writer, prog, msg string) {
	if structured.Load() {
		_ = clifmt.EncodeWarning(w, clifmt.WarningEnvelope{Prog: prog, Warning: msg})
		return
	}
	_, _ = fmt.Fprintf(w, "%s: warning: %s\n", prog, msg)
}

// sinkEntry is ONE active redirect. A nil w means "the default (os.Stderr)", so
// SetSink(nil) is an explicit redirect back to the default rather than an absent
// entry. Identity is the POINTER, never the writer: two redirects to the same
// writer — or two SetSink(nil) calls — are still distinct entries, so a restore
// can remove exactly the one it installed.
type sinkEntry struct{ w io.Writer }

// sink caches the ACTIVE redirect (the top of sinkStack) for lock-free reads on
// the warn path; nil means no redirect is installed, i.e. os.Stderr.
//
// The redirect machinery exists because os.Stderr is NOT always a safe place to
// write: under `ctxloom run` stderr IS the terminal the harness paints its TUI
// on, so an unconditional warning corrupts the display mid-frame —
// "run channel down (reconnecting)" landed straight on the TUI. A session that
// owns the terminal redirects the sink for its lifetime instead.
//
// sinkStack holds every redirect that has NOT yet been restored. It exists
// because restores are not guaranteed to arrive in LIFO order: an owner that
// finishes early must not take the channel away from an owner that is still
// painting, and a later restore must not resurrect a sink whose owner has
// already closed it. The stack is mutated only by SetSink/restore (rare); the
// hot read path never touches the mutex.
var (
	sink      atomic.Pointer[sinkEntry]
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
// finished one. It is idempotent — calling it twice pops nothing further.
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
	sink.Store(e)
	sinkMu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { popSink(e) }) }
}

// popSink removes e from the redirect stack (wherever it sits) and republishes
// whichever redirect is now on top — nil, meaning os.Stderr, when none is left.
func popSink(e *sinkEntry) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	for i := len(sinkStack) - 1; i >= 0; i-- {
		if sinkStack[i] == e {
			sinkStack = append(sinkStack[:i], sinkStack[i+1:]...)
			break
		}
	}
	if n := len(sinkStack); n > 0 {
		sink.Store(sinkStack[n-1])
		return
	}
	sink.Store(nil)
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

// warnSink resolves the current destination for the stderr-flavored helpers: the
// active redirect, or os.Stderr when none is installed (or when the active
// redirect is an explicit redirect back to the default).
func warnSink() io.Writer {
	if e := sink.Load(); e != nil && e.w != nil {
		return e.w
	}
	return os.Stderr
}

// Warn prints a "<prog>: warning: <msg>" line to the current sink (stderr by
// default — see SetSink).
func Warn(prog, format string, args ...any) {
	Fwarn(warnSink(), prog, format, args...)
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
	msg := fmt.Sprintf(format, args...)
	key := Line(prog, format, args...)
	onceMu.Lock()
	defer onceMu.Unlock()
	if _, seen := onceSeen[key]; seen {
		return
	}
	onceSeen[key] = struct{}{}
	fwarn(w, prog, msg)
}

// WarnOnce prints a "<prog>: warning: <msg>" line to the current sink (stderr
// by default — see SetSink) at most once per process for identical formatted
// content.
func WarnOnce(prog, format string, args ...any) {
	FwarnOnce(warnSink(), prog, format, args...)
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
