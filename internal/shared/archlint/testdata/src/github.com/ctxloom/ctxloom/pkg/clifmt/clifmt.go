// Package clifmt is the json-tags rule's stand-in for the real clifmt: just
// the sink signatures, function and method.
package clifmt

import "io"

// Format names an output format.
type Format string

// Option adjusts one render.
type Option func()

// Render is the package-level sink.
func Render(w io.Writer, v any, f Format, opts ...Option) error { return nil }

// Printer carries options across renders.
type Printer struct{}

// Render is the method sink: it keys as clifmt.Render too.
func (p *Printer) Render(w io.Writer, v any, f Format, opts ...Option) error { return nil }

// Unrelated takes a value second but renders nothing.
func Unrelated(w io.Writer, v any) error { return nil }
