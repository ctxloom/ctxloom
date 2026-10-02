// Package errwriter is the "errors are values" writer: many formatted writes,
// one error check at the end.
package errwriter

import (
	"fmt"
	"io"
)

// Writer wraps an io.Writer so a long run of formatted writes can be
// made without checking each call: the first write error is remembered
// and every subsequent write becomes a no-op. The caller inspects Err()
// once at the end. This is the "Errors are values" pattern
// (https://go.dev/blog/errors-are-values), which the bundle/profile/
// session/tasks render helpers rely on — they emit many lines to a
// single writer and propagate any failure up to the cobra RunE.
//
// Best-effort callers (fault-tolerant warning printers) write through a
// Writer too and simply skip the
// Err() check; the internal assignment to err keeps errcheck satisfied
// without scattering `_, _ =` across call sites. Those sites carry a
// comment explaining why the error is intentionally dropped.
type Writer struct {
	w   io.Writer
	err error
}

// New wraps w so writes capture their first error.
func New(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Printf is fmt.Fprintf against the wrapped writer, short-circuited once
// a prior write has failed.
func (e *Writer) Printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// Println is fmt.Fprintln against the wrapped writer, short-circuited
// once a prior write has failed.
func (e *Writer) Println(args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintln(e.w, args...)
}

// Print is fmt.Fprint against the wrapped writer, short-circuited once a
// prior write has failed.
func (e *Writer) Print(args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprint(e.w, args...)
}

// WriteRaw writes p verbatim to the wrapped writer, short-circuited once
// a prior write has failed. For emitting already-formatted bytes (e.g.
// marshaled YAML) without going through fmt.
//
// It earns its keep as an errcheck shim — a bare w.Write(data) at a call site
// would be flagged — but that is one line of delegation, not a copy:
// re-implementing Write's body here would put the short-circuit guard on e.err
// in two places on the same field.
func (e *Writer) WriteRaw(p []byte) {
	_, _ = e.Write(p)
}

// Write implements io.Writer, short-circuited once a prior write has failed, so
// a Writer can be handed to fmt.Fprintf / io.WriteString / clidiag.Fwarn and
// still accumulate its first error. The returned error mirrors Err(); callers
// using the Errors-are-values pattern ignore it and check Err() at the end.
func (e *Writer) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	var n int
	n, e.err = e.w.Write(p)
	return n, e.err
}

// Err returns the first write error encountered, or nil.
func (e *Writer) Err() error {
	return e.err
}
