package clifmt

import (
	"errors"
	"fmt"
	"strings"
)

// Format identifies one of the output encodings clifmt knows how to render.
type Format string

const (
	FormatJSON     Format = "json"
	FormatYAML     Format = "yaml"
	FormatTOML     Format = "toml"
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
)

// ErrUnsupportedFormat is the sentinel wrapped by every "unknown format"
// error clifmt returns. Callers should compare with errors.Is rather than
// matching on error strings, since the message includes the offending input.
var ErrUnsupportedFormat = errors.New("clifmt: unsupported format")

// Valid reports whether f is one of the five formats clifmt renders.
func (f Format) Valid() bool {
	switch f {
	case FormatJSON, FormatYAML, FormatTOML, FormatText, FormatMarkdown:
		return true
	default:
		return false
	}
}

func (f Format) String() string {
	return string(f)
}

// Structured reports whether f is one of the data-interchange formats a
// script parses (json, yaml, toml) rather than one of the two meant for a
// human terminal (text, markdown). Render uses all five; call sites that
// gate a machine-readable side channel — clidiag's structured-diagnostics
// switch is the first one — key off this instead of hand-rolling their own
// three-way OR.
func (f Format) Structured() bool {
	switch f {
	case FormatJSON, FormatYAML, FormatTOML:
		return true
	default:
		return false
	}
}

// ParseFormat maps a user-supplied string (e.g. a --format flag value) to a
// Format, accepting a couple of common aliases (yml, txt, md). Unrecognized
// input returns an error wrapping ErrUnsupportedFormat so callers can test
// for it with errors.Is instead of matching strings.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json":
		return FormatJSON, nil
	case "yaml", "yml":
		return FormatYAML, nil
	case "toml":
		return FormatTOML, nil
	case "text", "txt":
		return FormatText, nil
	case "markdown", "md":
		return FormatMarkdown, nil
	default:
		return "", fmt.Errorf("%w: %q (supported: json, yaml, toml, text, markdown)", ErrUnsupportedFormat, s)
	}
}
