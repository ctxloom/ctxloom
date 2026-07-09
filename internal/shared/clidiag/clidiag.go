// Package clidiag is the ctxloom family's stderr diagnostic convention:
// fault-tolerant warnings prefixed "<prog>: warning:". Per the fault-tolerance
// philosophy, components warn and continue rather than crash, so this is the
// one place that owns the prefix format. The prog is a parameter (not hardcoded
// to "ctxloom") so every binary stamps its own name.
package clidiag

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Line returns the "<prog>: warning: <msg>\n" line without writing it, for
// callers that need the string itself — dedup keys, or emission deferred to an
// aggregating writer. Warn and Fwarn are thin wrappers over it, so the format
// lives in exactly one place.
func Line(prog, format string, args ...any) string {
	return fmt.Sprintf(prog+": warning: "+format+"\n", args...)
}

// Fwarn writes a "<prog>: warning: <msg>" line to w. Best-effort: the write
// error is dropped (warnings never block), but a wrapping writer that records
// its own errors (e.g. iox.ErrWriter) still observes the failure.
func Fwarn(w io.Writer, prog, format string, args ...any) {
	_, _ = io.WriteString(w, Line(prog, format, args...))
}

// Warn prints a "<prog>: warning: <msg>" line to stderr.
func Warn(prog, format string, args ...any) {
	Fwarn(os.Stderr, prog, format, args...)
}

// onceSeen dedups WarnOnce/FwarnOnce lines per process, keyed by the full
// formatted line (Line's doc calls this out as its dedup-key use).
var (
	onceMu   sync.Mutex
	onceSeen = map[string]struct{}{}
)

// FwarnOnce writes the warning line to w at most once per process for
// identical formatted content. Repeat diagnostics from independently
// constructed components — e.g. every subsystem building its own profile
// loader and re-hitting the same unresolvable parent — collapse to a single
// line instead of spamming startup. Best-effort like Fwarn.
func FwarnOnce(w io.Writer, prog, format string, args ...any) {
	msg := Line(prog, format, args...)
	onceMu.Lock()
	defer onceMu.Unlock()
	if _, seen := onceSeen[msg]; seen {
		return
	}
	onceSeen[msg] = struct{}{}
	_, _ = io.WriteString(w, msg)
}

// WarnOnce prints a "<prog>: warning: <msg>" line to stderr at most once per
// process for identical formatted content.
func WarnOnce(prog, format string, args ...any) {
	FwarnOnce(os.Stderr, prog, format, args...)
}

// Warner binds a program name so callers that warn repeatedly don't repeat it.
//
//	warn := clidiag.Warner("taskloom")
//	warn.Warn("sync failed: %v", err)
type Warner string

// Warn prints a "<prog>: warning: <msg>" line to stderr for the bound prog.
func (p Warner) Warn(format string, args ...any) {
	Warn(string(p), format, args...)
}
