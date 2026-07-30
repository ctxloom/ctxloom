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

// allFormats is the canonical order the formats are named in, and the one
// enumeration SupportedFormats and UnsupportedFormatError read.
var allFormats = []Format{FormatJSON, FormatYAML, FormatTOML, FormatText, FormatMarkdown}

// SupportedFormats returns the formats clifmt renders, in the canonical order
// they are named to users. Callers that need to enumerate the set - a
// --format flag's help text, a table-driven test - read it here rather than
// writing their own copy of the list.
func SupportedFormats() []Format {
	return append([]Format(nil), allFormats...)
}

// UnsupportedFormatError builds the canonical error for input that names no
// known format: the ErrUnsupportedFormat sentinel, the offending input, and
// the supported list derived from SupportedFormats. Every producer of that
// error uses this - clifmt's own ParseFormat and Render, and first-party CLIs
// validating a Format they were handed - so the message cannot drift between
// them and adding a format cannot leave one of them naming the old set.
func UnsupportedFormatError(s string) error {
	names := make([]string, len(allFormats))
	for i, f := range allFormats {
		names[i] = string(f)
	}
	return fmt.Errorf("%w: %q (supported: %s)", ErrUnsupportedFormat, s, strings.Join(names, ", "))
}

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
		return "", UnsupportedFormatError(s)
	}
}
