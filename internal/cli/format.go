package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// Output formats accepted by the global --format flag.
const (
	formatText = "text"
	formatJSON = "json"
)

// emit renders a command's result in the format selected by the global
// --format flag. In json mode it marshals data — which should be the command's
// structured result, ideally an internal/operations result type — to the
// command's stdout. Otherwise it runs text, the human renderer.
//
// Commands build their result once and hand both forms here, so --format is a
// presentation choice and never a branch in business logic. This keeps every
// frontend (CLI, the VSCode companion, scripts) reading the same backend
// results: text for humans, json for machines.
func emit(cmd *cobra.Command, data any, text func() error) error {
	switch outputFormatOf(cmd) {
	case formatJSON:
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	case "", formatText:
		return text()
	default:
		return unknownFormatError(outputFormatOf(cmd))
	}
}

// outputFormatOf reads the inherited global --format value. An unset flag (e.g.
// a unit test that never registered it) reads as "" and is treated as text.
func outputFormatOf(cmd *cobra.Command) string {
	format, _ := cmd.Flags().GetString("format")
	return format
}

func unknownFormatError(format string) error {
	return fmt.Errorf("unknown format %q (supported: %s, %s)", format, formatText, formatJSON)
}

func init() {
	rootCmd.PersistentFlags().String("format", formatText, "Output format: text or json")
}
