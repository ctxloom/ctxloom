// Package clifmt is a shared, reflective output filter for first-party Go
// CLIs. A command hands over a struct (or slice of structs) and a Format;
// clifmt renders it — writing almost no per-command rendering code. The tag
// convention (json names and gates fields; the clifmt tag tunes display) is
// documented on parseJSONTag in tags.go and on hint in hints.go.
package clifmt

import (
	"fmt"
	"io"
)

// Render writes v to w in the given format. json/yaml/toml marshal v
// generically; text/markdown derive their output from struct reflection
// (see tags.go and hints.go for the json:/clifmt: tag convention). If v implements
// Renderer, it is consulted first and can take over any subset of formats.
// An unrecognized Format returns an error wrapping ErrUnsupportedFormat.
func Render(w io.Writer, v any, f Format) error {
	if r, ok := v.(Renderer); ok {
		handled, err := r.RenderCLI(w, f)
		if err != nil {
			return fmt.Errorf("clifmt: custom renderer: %w", err)
		}
		if handled {
			return nil
		}
	}

	spec, ok := specFor(f)
	if !ok {
		return UnsupportedFormatError(string(f))
	}
	return spec.render(w, v)
}
