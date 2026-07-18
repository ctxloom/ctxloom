package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Output formats accepted by the global --format flag. These two remain
// distinct string constants (not clifmt.Format aliases) because a handful of
// streaming commands — session watch, plan watch, run's structured REPL/chat
// event stream — parse --format themselves via their own text/json-only
// switch (see unknownFormatError below) rather than going through emit();
// they render one event at a time and structurally can't hand clifmt a
// single result value. Widening those to the full five formats is out of
// scope here.
const (
	formatText = "text"
	formatJSON = "json"
)

// unknownFormatError is the error the streaming commands above return for
// any --format value outside their own text/json pair. emit() has its own,
// wider error (clifmt.ParseFormat, via resolveFormat) covering all five
// formats.
func unknownFormatError(format string) error {
	return fmt.Errorf("unknown format %q (supported: %s, %s)", format, formatText, formatJSON)
}

// emit renders a command's result in the format selected by the global
// --format flag: json/yaml/toml/markdown all delegate to clifmt.Render,
// which marshals (json/yaml/toml) or reflects (markdown) data directly — no
// per-command renderer needed. text is the one format with an escape hatch:
// when the caller supplies a bespoke human renderer (text != nil), that runs
// unchanged, exactly as before clifmt existed; a nil text falls back to
// clifmt's own reflective text render, so a new call site can route through
// emit() with zero rendering code at all.
//
// Commands build their result once and hand both forms here, so --format is a
// presentation choice and never a branch in business logic. This keeps every
// frontend (CLI, the VSCode companion, scripts) reading the same backend
// results.
func emit(cmd *cobra.Command, data any, text func() error) error {
	format, err := resolveFormat(cmd)
	if err != nil {
		return err
	}
	if format == clifmt.FormatText && text != nil {
		return text()
	}
	return clifmt.Render(cmd.OutOrStdout(), data, format)
}

// resolveFormat reads the inherited global --format value and parses it via
// clifmt. An unset flag (e.g. a unit test that never registered it) reads as
// "" and is treated as text, matching emit()'s pre-clifmt default. Any other
// unrecognized value is an error wrapping clifmt.ErrUnsupportedFormat.
func resolveFormat(cmd *cobra.Command) (clifmt.Format, error) {
	raw := outputFormatOf(cmd)
	if raw == "" {
		return clifmt.FormatText, nil
	}
	return clifmt.ParseFormat(raw)
}

// outputFormatOf reads the raw inherited --format flag value, unparsed. An
// unset flag (e.g. a unit test that never registered it) reads as "".
func outputFormatOf(cmd *cobra.Command) string {
	format, _ := cmd.Flags().GetString("format")
	return format
}

func init() {
	rootCmd.PersistentFlags().String("format", formatText,
		"Output format: json, yaml, toml, text, or markdown")
}
