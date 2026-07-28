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
	"sync"
	"sync/atomic"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Line returns the "<prog>: warning: <msg>\n" line without writing it, for
// callers that need the string itself — dedup keys, or emission deferred to an
// aggregating writer. It always returns the human line, even in structured
// mode: WarnOnce/FwarnOnce's dedup key (see onceSeen below) needs one stable identity
// per distinct message regardless of which wire shape actually gets written.
func Line(prog, format string, args ...any) string {
	return fmt.Sprintf(prog+": warning: "+format+"\n", args...)
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

// sink is the process-wide destination for the stderr-flavored helpers
// (Warn/WarnOnce). It is a pointer so SetSink can swap it atomically;
// nil means "the default", os.Stderr.
//
// It exists because os.Stderr is NOT always a safe place to write: under
// `ctxloom run` stderr IS the terminal the harness paints its TUI on, so an
// unconditional warning corrupts the display mid-frame (large-album — "run
// channel down (reconnecting)" landed straight on the TUI). A session that
// owns the terminal redirects the sink for its lifetime instead.
var sink atomic.Pointer[io.Writer]

// SetSink redirects Warn/WarnOnce to w for the rest of the process, and
// returns a restore func that puts the previous sink back. A nil w restores the
// default (os.Stderr) — never installs a nil writer.
//
// Only Warn/WarnOnce move; the explicit Fwarn/FwarnOnce writers are
// untouched, because a caller that named its own writer already chose.
func SetSink(w io.Writer) (restore func()) {
	prev := sink.Load()
	if w == nil {
		sink.Store(nil)
	} else {
		sink.Store(&w)
	}
	return func() { sink.Store(prev) }
}

// warnSink resolves the current destination for the stderr-flavored helpers.
func warnSink() io.Writer {
	if w := sink.Load(); w != nil {
		return *w
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
// pattern (T9/R1, "exit 0 on a real failure"): that shape prints the same
// warnings but always returns nil, so cli.Execute never learns the command
// failed. WarnErrors prints identically, then returns a non-nil error when
// errs is non-empty (nil when it's empty), so the caller can simply
// `return clidiag.WarnErrors(prog, result.Errors)` and let cli.Execute's
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
