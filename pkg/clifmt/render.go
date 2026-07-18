// Package clifmt is a shared, reflective output filter for first-party Go
// CLIs. A command hands over a struct (or slice of structs) and a Format;
// clifmt renders it — writing almost no per-command rendering code. See the
// package README for the full tag convention and a worked example.
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

	switch f {
	case FormatJSON:
		return renderJSON(w, v)
	case FormatYAML:
		return renderYAML(w, v)
	case FormatTOML:
		return renderTOML(w, v)
	case FormatText:
		return renderText(w, v)
	case FormatMarkdown:
		return renderMarkdown(w, v)
	default:
		return fmt.Errorf("%w: %q (supported: json, yaml, toml, text, markdown)", ErrUnsupportedFormat, f)
	}
}
