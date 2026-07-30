package clifmt

import (
	"errors"
	"fmt"
	"io"
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

// formatSpec is the single declaration of one output format: its canonical
// name, the extra input spellings ParseFormat accepts for it, whether it is a
// data-interchange format a script parses rather than one meant for a human
// terminal, and the renderer Render dispatches to.
type formatSpec struct {
	format     Format
	aliases    []string
	structured bool
	render     func(io.Writer, any) error
}

// formatTable is the ONE enumeration of clifmt's format set, in the canonical
// order the formats are named to users. Valid, Structured, ParseFormat,
// Render, SupportedFormats and UnsupportedFormatError all read it, so adding a
// format is one entry here and none of them can be left behind. Previously
// each kept its own parallel list and nothing failed when they disagreed.
var formatTable = []formatSpec{
	{format: FormatJSON, structured: true, render: renderJSON},
	{format: FormatYAML, aliases: []string{"yml"}, structured: true, render: renderYAML},
	{format: FormatTOML, structured: true, render: renderTOML},
	{format: FormatText, aliases: []string{"txt"}, render: renderText},
	{format: FormatMarkdown, aliases: []string{"md"}, render: renderMarkdown},
}

// formatByName maps every accepted input spelling — canonical names and
// aliases alike — to its Format, derived from formatTable so ParseFormat
// cannot know a format the rest of the package does not.
var formatByName = func() map[string]Format {
	m := make(map[string]Format, len(formatTable)*2)
	for _, spec := range formatTable {
		m[string(spec.format)] = spec.format
		for _, a := range spec.aliases {
			m[a] = spec.format
		}
	}
	return m
}()

// specFor returns f's entry in formatTable, or false if f names no format.
func specFor(f Format) (formatSpec, bool) {
	for _, spec := range formatTable {
		if spec.format == f {
			return spec, true
		}
	}
	return formatSpec{}, false
}

// SupportedFormats returns the formats clifmt renders, in the canonical order
// they are named to users. Callers that need to enumerate the set - a
// --format flag's help text, a table-driven test - read it here rather than
// writing their own copy of the list.
func SupportedFormats() []Format {
	out := make([]Format, len(formatTable))
	for i, spec := range formatTable {
		out[i] = spec.format
	}
	return out
}

// UnsupportedFormatError builds the canonical error for input that names no
// known format: the ErrUnsupportedFormat sentinel, the offending input, and
// the supported list derived from SupportedFormats. Every producer of that
// error uses this - clifmt's own ParseFormat and Render, and first-party CLIs
// validating a Format they were handed - so the message cannot drift between
// them and adding a format cannot leave one of them naming the old set.
func UnsupportedFormatError(s string) error {
	formats := SupportedFormats()
	names := make([]string, len(formats))
	for i, f := range formats {
		names[i] = string(f)
	}
	return fmt.Errorf("%w: %q (supported: %s)", ErrUnsupportedFormat, s, strings.Join(names, ", "))
}

// Valid reports whether f is one of the formats clifmt renders.
func (f Format) Valid() bool {
	_, ok := specFor(f)
	return ok
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
	spec, ok := specFor(f)
	return ok && spec.structured
}

// ParseFormat maps a user-supplied string (e.g. a --format flag value) to a
// Format, accepting a couple of common aliases (yml, txt, md). Unrecognized
// input returns an error wrapping ErrUnsupportedFormat so callers can test
// for it with errors.Is instead of matching strings.
func ParseFormat(s string) (Format, error) {
	if f, ok := formatByName[strings.ToLower(strings.TrimSpace(s))]; ok {
		return f, nil
	}
	return "", UnsupportedFormatError(s)
}
