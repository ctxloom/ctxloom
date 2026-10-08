// Package clifmt renders a command's result for a CLI. A command hands over
// a value, normally a struct or a slice of structs (its data contract), and a
// Format; clifmt renders it with almost no per-command rendering code.
//
// The json tags set the machine shape (json, yaml and toml all follow them);
// the text and markdown views are derived from the same contract, tuned by
// one `clifmt:"…"` hint tag (see hint in hints.go). A command that wants a
// view of its own passes an Option: a per-type or per-path view returning a
// Doc (ViewFor, At), or raw bytes for one format (WithWriter). Options that
// apply to every call live on a Printer.
package clifmt

import "io"

// Render writes v to w in format f. json/yaml/toml marshal v through its json
// contract; text/markdown write the Doc derived from v, with any custom views
// in opts applied. An unrecognized Format returns an error wrapping
// ErrUnsupportedFormat.
func Render(w io.Writer, v any, f Format, opts ...Option) error {
	var p Printer
	return p.Render(w, v, f, opts...)
}
