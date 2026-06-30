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

// Warner binds a program name so callers that warn repeatedly don't repeat it.
//
//	warn := clidiag.Warner("taskloom")
//	warn.Warn("sync failed: %v", err)
type Warner string

// Warn prints a "<prog>: warning: <msg>" line to stderr for the bound prog.
func (p Warner) Warn(format string, args ...any) {
	Warn(string(p), format, args...)
}
