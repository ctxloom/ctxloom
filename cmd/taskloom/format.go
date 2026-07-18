package main

import (
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// The global --format flag (json/yaml/toml/text/markdown, default text) routes
// every command's output through the shared cliemit filter — see cmd/ctxloom
// for the same pattern. Commands call cliemit.Emit/Resolve directly.
func init() {
	rootCmd.PersistentFlags().String("format", string(clifmt.FormatText),
		"Output format: json, yaml, toml, text, or markdown")
}
