// Package clifmt is a shared, reflective output filter for first-party Go
// CLIs. A command hands over a struct (or slice of structs) and a Format;
// clifmt renders it — writing almost no per-command rendering code. The tag
// convention (json names and gates fields; label/col tune display) is
// documented on parseJSONTag, resolveLabel and resolveCol in tags.go.
package clifmt

import (
	"fmt"
	"io"
)

// Render writes v to w in the given format. json/yaml/toml marshal v
// generically; text/markdown derive their output from struct reflection
// (see tags.go for the json:/label:/col: tag convention). If v implements
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
