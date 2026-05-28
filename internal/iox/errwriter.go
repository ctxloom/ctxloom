// Package iox holds small io helpers shared across ctxloom packages.
package iox

import (
	"fmt"
	"io"
)

// ErrWriter wraps an io.Writer so a long run of formatted writes can be
// made without checking each call: the first write error is remembered
// and every subsequent write becomes a no-op. The caller inspects Err()
// once at the end. This is the "Errors are values" pattern
// (https://go.dev/blog/errors-are-values), which the bundle/profile/
// session/tasks render helpers rely on — they emit many lines to a
// single writer and propagate any failure up to the cobra RunE.
//
// Best-effort callers (fault-tolerant warning printers, the interactive
// session picker) write through an ErrWriter too and simply skip the
// Err() check; the internal assignment to err keeps errcheck satisfied
// without scattering `_, _ =` across call sites. Those sites carry a
// comment explaining why the error is intentionally dropped.
type ErrWriter struct {
	w   io.Writer
	err error
}

// NewErrWriter wraps w so writes capture their first error.
func NewErrWriter(w io.Writer) *ErrWriter {
	return &ErrWriter{w: w}
}

// Printf is fmt.Fprintf against the wrapped writer, short-circuited once
// a prior write has failed.
func (e *ErrWriter) Printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// Println is fmt.Fprintln against the wrapped writer, short-circuited
// once a prior write has failed.
func (e *ErrWriter) Println(args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintln(e.w, args...)
}

// Print is fmt.Fprint against the wrapped writer, short-circuited once a
// prior write has failed.
func (e *ErrWriter) Print(args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprint(e.w, args...)
}

// WriteRaw writes p verbatim to the wrapped writer, short-circuited once
// a prior write has failed. For emitting already-formatted bytes (e.g.
// marshaled YAML) without going through fmt.
func (e *ErrWriter) WriteRaw(p []byte) {
	if e.err != nil {
		return
	}
	_, e.err = e.w.Write(p)
}

// Err returns the first write error encountered, or nil.
func (e *ErrWriter) Err() error {
	return e.err
}
