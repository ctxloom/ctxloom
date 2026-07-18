package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
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
// --format flag. It delegates to the cross-binary cliemit filter (shared with
// cmd/taskloom and cmd/ltk) so the emit()/resolve pair is defined once:
// json/yaml/toml/markdown go through clifmt.Render, text runs the bespoke human
// closure (or, when nil, clifmt's reflective text render).
//
// Commands build their result once and hand both forms here, so --format is a
// presentation choice and never a branch in business logic. This keeps every
// frontend (CLI, the VSCode companion, scripts) reading the same backend
// results.
func emit(cmd *cobra.Command, data any, text func() error) error {
	return cliemit.Emit(cmd, data, text)
}

// resolveFormat reads the inherited global --format value and parses it via
// clifmt (through the shared cliemit filter). An unset flag reads as "" and is
// treated as text; any other unrecognized value is an error wrapping
// clifmt.ErrUnsupportedFormat.
func resolveFormat(cmd *cobra.Command) (clifmt.Format, error) {
	return cliemit.Resolve(cmd)
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
